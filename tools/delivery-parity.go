// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build ignore

// delivery-parity reads the PUBLISHED OpenAPI document — the committed file, not the
// in-memory registry — and fails when an endpoint has no CLI command, or a command has no
// endpoint. Reading the committed document is what makes the check able to fail: comparing
// the registry to itself would pass by construction (K7, I16).
//
// Usage: go run ./tools/delivery-parity.go [-doc docs/delivery/openapi.json]
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

func main() {
	doc := "docs/delivery/openapi.json"
	for i, arg := range os.Args[1:] {
		if arg == "-doc" && i+2 <= len(os.Args[1:]) {
			doc = os.Args[i+2]
		}
	}

	documented, err := operationIDs(doc)
	if err != nil {
		fail("read %s: %v\n  Suggested action: run `make delivery-generate` to publish the document.", doc, err)
	}
	commands, err := cliOperations()
	if err != nil {
		fail("list the CLI's operations: %v\n  Suggested action: run `go build ./cmd/gitea-delivery` and fix the build first.", err)
	}

	var problems []string
	for _, id := range sortedKeys(documented) {
		if _, ok := commands[id]; !ok {
			problems = append(problems, fmt.Sprintf(
				"endpoint %q is published but has no CLI command.\n  Suggested action: run `make delivery-generate` and commit cmd/gitea-delivery/generated_client.go.", id))
		}
	}
	for _, id := range sortedKeys(commands) {
		if _, ok := documented[id]; !ok {
			problems = append(problems, fmt.Sprintf(
				"CLI command %q calls operation %q, which the published document does not declare.\n  Suggested action: run `make delivery-generate` and commit docs/delivery/openapi.json.", commands[id], id))
		}
	}

	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "delivery-parity:", p)
		}
		os.Exit(1)
	}
	fmt.Printf("delivery-parity: %d published endpoints, %d CLI commands, all matched\n", len(documented), len(commands))
}

func operationIDs(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for path, methods := range doc.Paths {
		for method, op := range methods {
			if op.OperationID == "" {
				return nil, fmt.Errorf("%s %s has no operationId", strings.ToUpper(method), path)
			}
			out[op.OperationID] = strings.ToUpper(method) + " " + path
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the document declares no endpoint at all")
	}
	return out, nil
}

func cliOperations() (map[string]string, error) {
	cmd := exec.Command("go", "run", "./cmd/gitea-delivery", "--list-operations")
	raw, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		id, name, found := strings.Cut(line, "\t")
		if !found {
			return nil, fmt.Errorf("unreadable --list-operations line %q", line)
		}
		out[id] = name
	}
	return out, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "delivery-parity: "+format+"\n", args...)
	os.Exit(1)
}
