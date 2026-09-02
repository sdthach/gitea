// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build ignore

// delivery-spoke-check enforces F2: every edit the fork makes to an upstream file is a
// single-line delegation into the hub. It diffs the working tree against the upstream pin
// and fails on any upstream file that is not a declared spoke, on any spoke over its line
// budget, and on any deleted line — a rewritten upstream line is not a delegation.
//
// Usage: go run ./tools/delivery-spoke-check.go [-ref <pin>]
package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// defaultPin is the upstream commit the fork branches from (F8).
const defaultPin = "ee8f2b4039ef"

// spoke is one permitted upstream edit. Budget counts inserted lines; a delegation needs
// its import line as well as its call, which is why some budgets are above one.
type spoke struct {
	Budget int
	Why    string
}

var spokes = map[string]spoke{
	"routers/init.go":                  {4, "hub mount (F3/F6/M5) and API namespace mount (F3), each one call plus its import"},
	"routers/web/web.go":               {2, "web route registration beside /milestones (F13), one call plus its import"},
	"models/secret/secret.go":          {2, "secret narrowing tail call in GetSecretsOfTask (F4), one call plus its import"},
	"templates/base/head_navbar.tmpl":  {1, "one navigation entry delegating to a hub template (F13)"},
	"templates/repo/release/list.tmpl": {1, "one line delegating to a hub template that badges each release with the environments holding it (SC13)"},
	"templates/projects/view.tmpl":     {2, "one swimlane block delegating to a hub template (D2), plus the blank line separating it"},
	"routers/web/repo/projects.go":     {2, "the swimlane feature flag in the project page's data (D2), one assignment plus its import"},
	"models/unit/unit.go":              {1, "at most one unit enum entry (F2); unused at slice 3"},
	// The other spokes are tail calls, which are one statement and so one line. This one is
	// a GUARD inside CreateTaskForRunner's candidate loop: it has to skip the job, and gofmt
	// renders `if cond { continue }` as three lines (measured: gofmt expands a one-line if
	// body unconditionally). Import plus guard is therefore four, not two.
	"models/actions/task.go": {4, "task-assignment gate (F5e), added by slice 6: one import plus a three-line `if ... { continue }` guard"},
	".gitignore":             {1, "ignores the gitignored planning directory; carries no fork logic"},
	"Makefile":               {2, "one -include line per spoke makefile, Makefile.delivery and Makefile.themes, so neither adds a target to it"},
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
	touched := 0
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

		s, ok := spokes[path]
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"%s is an upstream file the fork edited but did not declare as a spoke (+%d/-%d).\n"+
					"  Suggested action: move the logic into models/delivery, services/delivery or routers/*/delivery and leave a one-line delegation, or add the file to the spoke table with its reason.",
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
			fmt.Fprintln(os.Stderr, "delivery-spoke-check:", p)
		}
		os.Exit(1)
	}
	fmt.Printf("delivery-spoke-check: %d upstream file(s) edited against %s, every one a declared spoke within budget\n", touched, ref)
}

func parseCount(field string) int {
	n, err := strconv.Atoi(field)
	if err != nil {
		return 0 // "-" marks a binary file, which no spoke is
	}
	return n
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "delivery-spoke-check: "+format+"\n", args...)
	os.Exit(1)
}
