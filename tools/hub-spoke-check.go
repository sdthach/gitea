// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build ignore

// hub-spoke-check enforces the spoke rule: every edit the fork makes to an upstream
// file is a single-line delegation into the hub. It diffs the working tree against the upstream pin
// and fails on any upstream file that is neither a declared spoke nor a declared override, on any spoke over its line
// budget, and on any deleted line — a rewritten upstream line is not a delegation.
//
// Usage: go run ./tools/hub-spoke-check.go [-ref <pin>]
package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// defaultPin is the upstream commit the fork branches from.
const defaultPin = "5ec9714dde"

// spoke is one permitted upstream edit. Budget counts inserted lines; a delegation needs
// its import line as well as its call, which is why some budgets are above one.
type spoke struct {
	Budget int
	Why    string
}

var spokes = map[string]spoke{
	"routers/init.go":                  {6, "hub mount and two API namespace mounts, each one call plus its import"},
	"routers/web/web.go":               {2, "web route registration beside /milestones, one call plus its import"},
	"models/secret/secret.go":          {2, "secret narrowing tail call in GetSecretsOfTask, one call plus its import"},
	"templates/base/head_navbar.tmpl":  {1, "one navigation entry delegating to a hub template"},
	"templates/repo/release/list.tmpl": {1, "one line delegating to a hub template that badges each release with the environments holding it"},
	"templates/projects/view.tmpl":     {2, "one swimlane block delegating to a hub template, plus the blank line separating it"},
	"routers/web/repo/projects.go":     {2, "the swimlane feature flag in the project page's data, one assignment plus its import"},
	"models/unit/unit.go":              {1, "at most one unit enum entry"},
	// The other spokes are tail calls, which are one statement and so one line. This one is
	// a GUARD inside CreateTaskForRunner's candidate loop: it has to skip the job, and gofmt
	// renders `if cond { continue }` as three lines (measured: gofmt expands a one-line if
	// body unconditionally). Import plus guard is therefore four, not two.
	"models/actions/task.go": {4, "task-assignment gate: one import plus a three-line `if ... { continue }` guard"},
	".gitignore":             {4, "ignores the planning directory and the generated theme preview, with the preview's comment and separator; carries no fork logic"},
	"Makefile":               {2, "one -include line per spoke makefile, Makefile.hub and Makefile.themes, so neither adds a target to it"},
}

// overrides are upstream files the fork replaces wholesale rather than delegating from.
// They are exempt from the budget and the no-deletion rule, but not from being declared.
var overrides = map[string]string{
	"web_src/css/base.css":                     "GitHub default styling (b40c841c1e)",
	"web_src/css/modules/label.css":            "GitHub default styling (b40c841c1e)",
	"web_src/css/themes/theme-gitea-dark.css":  "GitHub default styling (b40c841c1e)",
	"web_src/css/themes/theme-gitea-light.css": "GitHub default styling (b40c841c1e)",
}

func main() {
	ref := defaultPin
	args := os.Args[1:]
	for i, arg := range args {
		if arg == "-ref" && i+1 < len(args) {
			ref = args[i+1]
		}
	}

	out, err := exec.Command("git", "diff", "--numstat", "--diff-filter=MDR", ref, "--", ".").Output()
	if err != nil {
		fail("git diff against %s failed: %v\n  Suggested action: fetch the pin, or pass -ref with a commit this clone has.", ref, err)
	}

	var problems []string
	touched, overridden := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			problems = append(problems, fmt.Sprintf("unreadable numstat line %q", line))
			continue
		}
		added, deleted, path := parseCount(fields[0]), parseCount(fields[1]), fields[2]
		touched++

		if _, ok := overrides[path]; ok {
			overridden++
			continue
		}
		s, ok := spokes[path]
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"%s is an upstream file the fork edited but did not declare as a spoke (+%d/-%d).\n"+
					"  Suggested action: move the logic into models/hub, models/deployments, services/hub, services/planning, services/deployments or routers/*/{hub,planning,deployments} and leave a one-line delegation, or add the file to the spoke table with its reason.",
				path, added, deleted))
			continue
		}
		if deleted > 0 {
			problems = append(problems, fmt.Sprintf(
				"%s deletes %d upstream line(s); a delegation inserts, it does not rewrite.\n"+
					"  Suggested action: restore the original line and insert the delegation beside it.", path, deleted))
		}
		if added > s.Budget {
			problems = append(problems, fmt.Sprintf(
				"%s inserts %d lines, its budget is %d (%s).\n"+
					"  Suggested action: collapse the insertion into a single call into the hub and move the rest into a fork package.",
				path, added, s.Budget, s.Why))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "hub-spoke-check:", p)
		}
		os.Exit(1)
	}
	fmt.Printf("hub-spoke-check: %d upstream file(s) edited against %s, %d declared overrides, every other one a declared spoke within budget\n", touched, ref, overridden)
}

func parseCount(field string) int {
	n, err := strconv.Atoi(field)
	if err != nil {
		return 0 // "-" marks a binary file, which no spoke is
	}
	return n
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "hub-spoke-check: "+format+"\n", args...)
	os.Exit(1)
}
