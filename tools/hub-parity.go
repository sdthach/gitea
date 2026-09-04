// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build ignore

// hub-parity reads the PUBLISHED OpenAPI document for one area, or by default both — the
// committed file, not the in-memory registry — and fails when an endpoint has no CLI command,
// or a command has no endpoint. Reading the committed document is what makes the check able to
// fail: comparing the registry to itself would pass by construction.
//
// Usage:
//
//	go run ./tools/hub-parity.go                  check both areas against their committed docs
//	go run ./tools/hub-parity.go -area planning    check only the planning area
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

type parityArea struct {
	binaryName string
	docPath    string
}

var parityAreas = map[string]parityArea{
	"planning":    {binaryName: "gitea-planning", docPath: "docs/planning/openapi.json"},
	"deployments": {binaryName: "gitea-deployments", docPath: "docs/deployments/openapi.json"},
}

func main() {
	areaFlag := ""
	for i, arg := range os.Args[1:] {
		if arg == "-area" && i+2 <= len(os.Args[1:]) {
			areaFlag = os.Args[i+2]
		}
	}

	selected := sortedParityAreaNames()
	if areaFlag != "" {
		if _, ok := parityAreas[areaFlag]; !ok {
			fail("unknown -area %q; accepted: %s", areaFlag, strings.Join(selected, ", "))
		}
		selected = []string{areaFlag}
	}

	failed := false
	for _, name := range selected {
		failed = checkArea(parityAreas[name]) || failed
	}
	if failed {
		os.Exit(1)
	}
}

func checkArea(a parityArea) bool {
	documented, err := operationIDs(a.docPath)
	if err != nil {
		fail("read %s: %v\n  Suggested action: run `make hub-generate` to publish the document.", a.docPath, err)
	}
	commands, err := cliOperations(a.binaryName)
	if err != nil {
		fail("list %s's operations: %v\n  Suggested action: run `go build ./cmd/%s` and fix the build first.", a.binaryName, err, a.binaryName)
	}

	var problems []string
	for _, id := range sortedKeys(documented) {
		if _, ok := commands[id]; !ok {
			problems = append(problems, fmt.Sprintf(
				"endpoint %q is published but has no CLI command.\n  Suggested action: run `make hub-generate` and commit cmd/%s/generated_client.go.", id, a.binaryName))
		}
	}
	for _, id := range sortedKeys(commands) {
		if _, ok := documented[id]; !ok {
			problems = append(problems, fmt.Sprintf(
				"CLI command %q calls operation %q, which the published document does not declare.\n  Suggested action: run `make hub-generate` and commit %s.", commands[id], id, a.docPath))
		}
	}

	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "hub-parity:", p)
		}
		return true
	}
	fmt.Printf("hub-parity: %s: %d published endpoints, %d CLI commands, all matched\n", a.binaryName, len(documented), len(commands))
	return false
}

func sortedParityAreaNames() []string {
	names := make([]string, 0, len(parityAreas))
	for name := range parityAreas {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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

func cliOperations(binaryName string) (map[string]string, error) {
	cmd := exec.Command("go", "run", "./cmd/"+binaryName, "--list-operations")
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
	fmt.Fprintf(os.Stderr, "hub-parity: "+format+"\n", args...)
	os.Exit(1)
}
