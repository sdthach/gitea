// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"testing"

	"gitea.dev/models/db"
	hub_model "gitea.dev/models/hub"
	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeploymentsValidateDeployment(t *testing.T) {
	valid := func() *Deployment {
		return &Deployment{RepoID: 1, Environment: "qa", ReleaseTag: "v1", RunID: 7, Status: "success"}
	}

	require.NoError(t, ValidateDeployment(valid()))

	cases := map[string]func(*Deployment){
		"no repository": func(d *Deployment) { d.RepoID = 0 },
		"no environment": func(d *Deployment) {
			d.Environment = "   "
		},
		"no release tag": func(d *Deployment) { d.ReleaseTag = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := valid()
			mutate(d)
			err := ValidateDeployment(d)
			require.Error(t, err, "the rejection is what keeps an unusable row out of an append-only table")

			var hubErr *hub_model.Error
			require.ErrorAs(t, err, &hubErr)
			assert.NotEmpty(t, hubErr.SuggestedAction, "every error carries a suggested next action")
		})
	}
}

// TestDeploymentsDeploymentsAreAppendOnly: one row per deploy, and the table is append-only.
func TestDeploymentsDeploymentsAreAppendOnly(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	// v1 to qa, then v2 to qa, then v1 to qa again — three distinct runs.
	for i, d := range []*Deployment{
		{RepoID: 1, Environment: "qa", ReleaseTag: "v1", RunID: 101, Status: "success"},
		{RepoID: 1, Environment: "qa", ReleaseTag: "v2", RunID: 102, Status: "success"},
		{RepoID: 1, Environment: "qa", ReleaseTag: "v1", RunID: 103, Status: "success"},
	} {
		require.NoError(t, AppendDeployment(ctx, d), "append %d", i)
	}

	rows, err := FindDeployments(ctx, builderEq("repo_id", int64(1)), "id ASC", 0)
	require.NoError(t, err)
	require.Len(t, rows, 3,
		"three deploys leave three rows; an implementation that upserts per (release, environment) would leave two")
	assert.Equal(t, []string{"v1", "v2", "v1"}, []string{rows[0].ReleaseTag, rows[1].ReleaseTag, rows[2].ReleaseTag})

	// A run reporting several status changes still leaves one row for that run, so the
	// count above measures deploys rather than notifications — but that one row's Status is
	// refreshed to the latest report, or exclusiveLockCheck would read a finished run as
	// forever busy.
	require.NoError(t, AppendDeployment(ctx, &Deployment{RepoID: 1, Environment: "qa", ReleaseTag: "v1", RunID: 103, Status: "failure"}))
	rows, err = FindDeployments(ctx, builderEq("repo_id", int64(1)), "id ASC", 0)
	require.NoError(t, err)
	require.Len(t, rows, 3, "re-observing a run appends nothing")
	assert.Equal(t, "failure", rows[2].Status, "the row's Status tracks the run's latest reported state")

	// Re-saving an existing row is what an update looks like through the model, and it is
	// refused rather than silently applied.
	existing := rows[0]
	err = AppendDeployment(ctx, existing)
	require.Error(t, err, "an append-only table has no update path")
	var hubErr *hub_model.Error
	require.ErrorAs(t, err, &hubErr)
	assert.Contains(t, hubErr.Message, "append-only")
	assert.NotEmpty(t, hubErr.SuggestedAction)
}

func TestDeploymentsFindDeploymentsLimitsWithoutOffset(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	for i := int64(1); i <= 5; i++ {
		require.NoError(t, db.Insert(ctx, &Deployment{
			RepoID: 2, Environment: "prod", ReleaseTag: "v" + string(rune('0'+i)), RunID: 200 + i, Status: "success",
		}))
	}

	page, err := FindDeployments(ctx, builderEq("repo_id", int64(2)), "id ASC", 2)
	require.NoError(t, err)
	assert.Len(t, page, 2, "the limit bounds the page; the position comes from the cursor, never an offset")

	all, err := FindDeployments(ctx, builderEq("repo_id", int64(2)), "id ASC", 0)
	require.NoError(t, err)
	assert.Len(t, all, 5, "limit 0 means no limit")
}
