// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"testing"

	"gitea.dev/models/db"
	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSeedPlan(t *testing.T) {
	assert.Equal(t, DefaultEnvironments, seedPlan(DefaultEnvironments, nil),
		"a fresh database needs every default row")

	assert.Empty(t, seedPlan(DefaultEnvironments, names(DefaultEnvironments)),
		"re-running changes nothing")

	assert.Equal(t, []SeededEnvironment{{Name: "qa", SortOrder: 20}},
		seedPlan(DefaultEnvironments[:2], []string{"dev"}),
		"only the missing row is planned")

	assert.Empty(t, seedPlan(DefaultEnvironments[:1], []string{"DEV"}),
		"names are identifiers, matched case-insensitively")
}

func names(envs []SeededEnvironment) []string {
	out := make([]string, len(envs))
	for i, e := range envs {
		out[i] = e.Name
	}
	return out
}

// TestSeedIsIdempotentAndRestoring is SC 31: starting twice seeds no duplicate row and does
// not overwrite an edited row; deleting a seeded row and restarting restores it.
func TestSeedIsIdempotentAndRestoring(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	require.NoError(t, Seed(ctx))
	first := defaults(t)
	require.Len(t, first, len(DefaultEnvironments))
	assert.Equal(t, []string{"dev", "qa", "uat", "staging", "prod"}, envNames(first),
		"the environment set is seeded in its configured order")
	for _, env := range first {
		assert.Equal(t, PolicyNone, env.ApprovalPolicy, "a new environment gates nothing (F5b)")
		assert.EqualValues(t, 1, env.RequiredApprovals)
	}

	// A user edits a seeded row.
	edited := first[len(first)-1]
	edited.ApprovalPolicy = PolicyOthersOnly
	edited.RequiredApprovals = 2
	edited.SortOrder = 999
	_, err := db.GetEngine(ctx).ID(edited.ID).Cols("approval_policy", "required_approvals", "sort_order").Update(edited)
	require.NoError(t, err)

	// A second start.
	require.NoError(t, Seed(ctx))
	second := defaults(t)
	assert.Len(t, second, len(DefaultEnvironments), "starting twice seeds no duplicate row")

	after, err := GetEnvironment(ctx, DefaultsRepoID, edited.Name)
	require.NoError(t, err)
	assert.Equal(t, PolicyOthersOnly, after.ApprovalPolicy, "an edited row is not overwritten")
	assert.EqualValues(t, 2, after.RequiredApprovals)
	assert.EqualValues(t, 999, after.SortOrder)

	// Deleting a seeded row and restarting restores it.
	_, err = db.GetEngine(ctx).ID(first[0].ID).Delete(new(Environment))
	require.NoError(t, err)
	require.Len(t, defaults(t), len(DefaultEnvironments)-1)

	require.NoError(t, Seed(ctx))
	restored := defaults(t)
	assert.Len(t, restored, len(DefaultEnvironments))
	got, err := GetEnvironment(ctx, DefaultsRepoID, first[0].Name)
	require.NoError(t, err)
	assert.Equal(t, first[0].SortOrder, got.SortOrder, "the restored row carries its configured order again")
}

func defaults(t *testing.T) []*Environment {
	t.Helper()
	rows := make([]*Environment, 0, 8)
	require.NoError(t, db.GetEngine(t.Context()).Where("repo_id = ?", DefaultsRepoID).OrderBy("sort_order ASC, id ASC").Find(&rows))
	return rows
}

func envNames(envs []*Environment) []string {
	out := make([]string, len(envs))
	for i, e := range envs {
		out[i] = e.Name
	}
	return out
}

// TestGetEnvironmentFallsBackToTheDefaultSet covers a repository that has declared no
// environment of its own.
func TestGetEnvironmentFallsBackToTheDefaultSet(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	require.NoError(t, Seed(ctx))

	env, err := GetEnvironment(ctx, 4242, "PROD")
	require.NoError(t, err)
	assert.Equal(t, DefaultsRepoID, env.RepoID)
	assert.Equal(t, "prod", env.Name)

	require.NoError(t, db.Insert(ctx, &Environment{RepoID: 4242, Name: "prod", SortOrder: 7, ApprovalPolicy: PolicyAnyApprover, RequiredApprovals: 1}))
	env, err = GetEnvironment(ctx, 4242, "prod")
	require.NoError(t, err)
	assert.EqualValues(t, 4242, env.RepoID, "the repository's own row wins over the default")

	_, err = GetEnvironment(ctx, 4242, "nowhere")
	require.Error(t, err)
	var hubErr *Error
	require.ErrorAs(t, err, &hubErr)
	assert.NotEmpty(t, hubErr.SuggestedAction)
}
