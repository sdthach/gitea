// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"

	"gitea.dev/models/db"
)

// SeededEnvironment is one row of the instance-wide default environment set.
type SeededEnvironment struct {
	Name      string
	SortOrder int64
}

// DefaultEnvironments is the environment set, in configured order. Order is configuration
// read at render time; nothing in the model expresses sequence (F9).
var DefaultEnvironments = []SeededEnvironment{
	{Name: "dev", SortOrder: 10},
	{Name: "qa", SortOrder: 20},
	{Name: "uat", SortOrder: 30},
	{Name: "staging", SortOrder: 40},
	{Name: "prod", SortOrder: 50},
}

// seedPlan reports which of the wanted rows are missing from existing. It is pure so the
// "re-running changes nothing" and "an edited row is not overwritten" properties are
// testable without a database.
func seedPlan(wanted []SeededEnvironment, existing []string) []SeededEnvironment {
	have := make(map[string]bool, len(existing))
	for _, name := range existing {
		have[NormalizeEnvironmentName(name)] = true
	}
	missing := make([]SeededEnvironment, 0, len(wanted))
	for _, w := range wanted {
		if !have[NormalizeEnvironmentName(w.Name)] {
			missing = append(missing, w)
		}
	}
	return missing
}

// Seed inserts any missing default environment. It runs at hub mount on every start and is
// an insert on the natural key (repo_id, name), never an update: re-running changes
// nothing, a row a user has edited is not overwritten, and deleting a seeded row and
// restarting restores it (M5, SC 31).
//
// It deliberately does not use RegisterModel's init-func hook, whose append is guarded by
// len(registeredInitFuncs) > 0 on a slice that starts empty, so a callback attached there
// is never invoked (M4).
func Seed(ctx context.Context) error {
	rows := make([]*Environment, 0, len(DefaultEnvironments))
	if err := db.GetEngine(ctx).Where("repo_id = ?", DefaultsRepoID).Find(&rows); err != nil {
		return err
	}
	existing := make([]string, 0, len(rows))
	for _, r := range rows {
		existing = append(existing, r.Name)
	}

	for _, want := range seedPlan(DefaultEnvironments, existing) {
		env := &Environment{
			RepoID:            DefaultsRepoID,
			Name:              NormalizeEnvironmentName(want.Name),
			SortOrder:         want.SortOrder,
			ApprovalPolicy:    PolicyNone,
			RequiredApprovals: 1,
		}
		if err := db.Insert(ctx, env); err != nil {
			return err
		}
	}
	return nil
}
