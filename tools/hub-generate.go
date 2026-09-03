// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build ignore

// hub-generate writes every generated hub artifact, for one area or, by default, both in
// sequence:
//
//	docs/<area>/openapi.json              the published OpenAPI 3 contract
//	docs/<area>/cli-reference.md          the CLI command reference, from its own --help
//	cmd/gitea-<area>/generated_client.go  the CLI's request layer, from the document
//
// CI runs it and then gates on `git diff --exit-code`: a CI that merely runs a generator
// proves nothing about what is committed.
//
// Usage:
//
//	go run ./tools/hub-generate.go                    write both areas' artifacts
//	go run ./tools/hub-generate.go -area planning      write only the planning area
//	go run ./tools/hub-generate.go -check              write to a scratch dir and diff
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	deploymentsv1 "gitea.dev/routers/api/deployments/v1"
	hubapi "gitea.dev/routers/api/hub"
	planningv1 "gitea.dev/routers/api/planning/v1"
	"gitea.dev/services/hub/cligen"
)

// area is one CLI binary's generated set. clientDir and clientPackage default to the binary's
// own main package when empty; planning's generated table instead lives in an importable
// client subpackage, so the integration suite can build the same command set the binary ships.
type area struct {
	binaryName    string
	docsDir       string
	clientDir     string
	clientPackage string
	openAPI       func() ([]byte, error)
	operations    func() []*hubapi.Operation
	schemas       func() map[string]map[string]any
}

var areas = map[string]area{
	"planning": {
		binaryName:    "gitea-planning",
		docsDir:       "docs/planning",
		clientDir:     "cmd/gitea-planning/client",
		clientPackage: "client",
		openAPI:       planningv1.OpenAPI,
		operations:    planningv1.Operations,
		schemas:       planningv1.Schemas,
	},
	"deployments": {
		binaryName: "gitea-deployments",
		docsDir:    "docs/deployments",
		openAPI:    deploymentsv1.OpenAPI,
		operations: deploymentsv1.Operations,
		schemas:    deploymentsv1.Schemas,
	},
}

func main() {
	check := flag.Bool("check", false, "regenerate into a scratch location and diff instead of writing")
	areaFlag := flag.String("area", "", "planning|deployments; empty runs both in sequence")
	flag.Parse()

	selected := sortedAreaNames()
	if *areaFlag != "" {
		if _, ok := areas[*areaFlag]; !ok {
			fmt.Fprintf(os.Stderr, "hub-generate: unknown -area %q; accepted: %s\n", *areaFlag, strings.Join(selected, ", "))
			os.Exit(2)
		}
		selected = []string{*areaFlag}
	}

	failed := false
	for _, name := range selected {
		failed = generateArea(name, areas[name], *check) || failed
	}
	if failed {
		os.Exit(1)
	}
}

func sortedAreaNames() []string {
	names := make([]string, 0, len(areas))
	for name := range areas {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func generateArea(name string, a area, check bool) bool {
	doc, err := a.openAPI()
	must(err)

	ops := a.operations()
	clientDir := a.clientDir
	if clientDir == "" {
		clientDir = "cmd/" + a.binaryName
	}
	client, err := cligen.RenderClient(ops, a.schemas(), a.clientPackage)
	must(err)

	// The command reference is rendered by running the CLI, so the generated request layer
	// has to be on disk before it can be produced.
	staged := map[string][]byte{
		a.docsDir + "/openapi.json":        doc,
		clientDir + "/generated_client.go": client,
	}
	failed := false
	for _, path := range sortedKeys(staged) {
		if check {
			failed = compare(path, staged[path]) || failed
			continue
		}
		write(path, staged[path])
	}

	reference, err := renderCommandReference(a, ops)
	must(err)
	referencePath := a.docsDir + "/cli-reference.md"
	if check {
		failed = compare(referencePath, reference) || failed
	} else {
		write(referencePath, reference)
	}
	_ = name
	return failed
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// compare reports whether the committed artifact differs from what the generator produces.
// CI gates on this, not on the generator merely running.
func compare(path string, content []byte) bool {
	existing, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(existing, content) {
		fmt.Fprintf(os.Stderr, "%s is out of date.\n  Suggested action: run `make hub-generate` and commit the result.\n", path)
		return true
	}
	return false
}

func write(path string, content []byte) {
	must(os.MkdirAll(filepath.Dir(path), 0o755))
	must(os.WriteFile(path, content, 0o644))
	fmt.Println("wrote", path)
}

// renderCommandReference builds the CLI's own command reference from its --help output, so
// agent-facing documentation is never hand-maintained beside the commands it describes.
func renderCommandReference(a area, ops []*hubapi.Operation) ([]byte, error) {
	var out bytes.Buffer
	fmt.Fprintf(&out, "<!-- Generated by tools/hub-generate.go from `%s --help`. Do not edit. -->\n", a.binaryName)
	fmt.Fprintf(&out, "# %s command reference\n\n", a.binaryName)

	root, err := helpOutput(a)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(&out, "## %s\n\n```\n", a.binaryName)
	out.WriteString(root)
	out.WriteString("```\n")

	names := cligen.CommandNames(ops)
	for _, name := range names {
		sub, err := helpOutput(a, name)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&out, "\n## %s %s\n\n```\n%s```\n", a.binaryName, name, sub)
	}
	return out.Bytes(), nil
}

func helpOutput(a area, args ...string) (string, error) {
	cmd := exec.Command("go", append([]string{"run", "./cmd/" + a.binaryName}, append(args, "--help")...)...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s --help: %w\n%s", a.binaryName, strings.Join(args, " "), err, raw)
	}
	return string(raw), nil
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "hub-generate:", err)
		os.Exit(1)
	}
}
