// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"context"
	"strings"

	"gitea.dev/models/db"
	"gitea.dev/modules/setting"
)

// SettingSection is the app.ini section the fork reads.
const SettingSection = "delivery"

// seededSortStep spaces the seeded rows so an operator can insert between two of them
// without renumbering either.
const seededSortStep = 10

// SeededEnvironment is one row of the instance-wide default environment set.
type SeededEnvironment struct {
	Name      string
	SortOrder int64
}

// SeededEnvironments reads the set a fresh instance starts with:
//
//	[delivery]
//	DEFAULT_ENVIRONMENTS = sandbox, live
//
// An unset key seeds nothing. Environment names are the operator's, not the fork's: no
// name carries meaning anywhere in the model, and order is the Predecessor chain each row
// declares, so there is no set the fork can pick on an operator's behalf.
func SeededEnvironments() []SeededEnvironment {
	if setting.CfgProvider == nil {
		return nil
	}
	raw := setting.CfgProvider.Section(SettingSection).Key("DEFAULT_ENVIRONMENTS").String()
	out := make([]SeededEnvironment, 0, 8)
	for name := range strings.SplitSeq(raw, ",") {
		if name = NormalizeEnvironmentName(name); name != "" {
			out = append(out, SeededEnvironment{Name: name, SortOrder: int64(len(out)+1) * seededSortStep})
		}
	}
	return out
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

// Seed inserts any of wanted that is missing. It runs at hub mount on every start and is
// an insert on the natural key (repo_id, name), never an update: re-running changes
// nothing, a row a user has edited is not overwritten, and deleting a seeded row and
// restarting restores it.
//
// It deliberately does not use RegisterModel's init-func hook, whose append is guarded by
// len(registeredInitFuncs) > 0 on a slice that starts empty, so a callback attached there
// is never invoked.
func Seed(ctx context.Context, wanted []SeededEnvironment) error {
	if len(wanted) == 0 {
		return nil
	}
	rows := make([]*Environment, 0, len(wanted))
	if err := db.GetEngine(ctx).Where("repo_id = ?", DefaultsRepoID).Find(&rows); err != nil {
		return err
	}
	existing := make([]string, 0, len(rows))
	for _, r := range rows {
		existing = append(existing, r.Name)
	}

	for _, want := range seedPlan(wanted, existing) {
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
