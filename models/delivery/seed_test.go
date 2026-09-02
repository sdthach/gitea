// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"testing"

	"gitea.dev/models/db"
	"gitea.dev/models/unittest"
	"gitea.dev/modules/setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// configuredSet stands in for whatever an operator writes in DEFAULT_ENVIRONMENTS. The
// names carry no meaning to the fork, which is the property these tests hold it to.
var configuredSet = []SeededEnvironment{
	{Name: "sandbox", SortOrder: 10},
	{Name: "live", SortOrder: 20},
}

func TestSeedPlan(t *testing.T) {
	assert.Equal(t, configuredSet, seedPlan(configuredSet, nil),
		"a fresh database needs every configured row")

	assert.Empty(t, seedPlan(configuredSet, names(configuredSet)),
		"re-running changes nothing")

	assert.Equal(t, []SeededEnvironment{{Name: "live", SortOrder: 20}},
		seedPlan(configuredSet, []string{"sandbox"}),
		"only the missing row is planned")

	assert.Empty(t, seedPlan(configuredSet[:1], []string{"SANDBOX"}),
		"names are identifiers, matched case-insensitively")
}

// TestSeededEnvironmentsIsConfiguration is the point of the key: an operator names their
// own environments, and an unset key seeds none.
func TestSeededEnvironmentsIsConfiguration(t *testing.T) {
	assert.Empty(t, SeededEnvironments(), "no config provider is the no-[delivery]-section case")

	previous := setting.CfgProvider
	t.Cleanup(func() { setting.CfgProvider = previous })

	cfg, err := setting.NewConfigProviderFromData("[delivery]\nDEFAULT_ENVIRONMENTS = Sandbox, live , \n")
	require.NoError(t, err)
	setting.CfgProvider = cfg
	assert.Equal(t, configuredSet, SeededEnvironments(),
		"names are normalized, blanks dropped, and order is the order given")

	cfg, err = setting.NewConfigProviderFromData("[delivery]\n")
	require.NoError(t, err)
	setting.CfgProvider = cfg
	assert.Empty(t, SeededEnvironments(), "an unset key seeds nothing")
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
	_, err := db.GetEngine(ctx).Where("repo_id = ?", DefaultsRepoID).Delete(new(Environment))
	require.NoError(t, err)

	require.NoError(t, Seed(ctx, nil), "no configured set seeds nothing")
	require.Empty(t, defaults(t))

	require.NoError(t, Seed(ctx, configuredSet))
	first := defaults(t)
	require.Len(t, first, len(configuredSet))
	assert.Equal(t, []string{"sandbox", "live"}, envNames(first),
		"the environment set is seeded in the order it was configured")
	for _, env := range first {
		assert.Equal(t, PolicyNone, env.ApprovalPolicy, "a new environment gates nothing (F5b)")
		assert.EqualValues(t, 1, env.RequiredApprovals)
		assert.False(t, env.RequireFullRelease, "and refuses no release kind")
	}

	// A user edits a seeded row.
	edited := first[len(first)-1]
	edited.ApprovalPolicy = PolicyOthersOnly
	edited.RequiredApprovals = 2
	edited.SortOrder = 999
	_, err = db.GetEngine(ctx).ID(edited.ID).Cols("approval_policy", "required_approvals", "sort_order").Update(edited)
	require.NoError(t, err)

	// A second start.
	require.NoError(t, Seed(ctx, configuredSet))
	second := defaults(t)
	assert.Len(t, second, len(configuredSet), "starting twice seeds no duplicate row")

	after, err := GetEnvironment(ctx, DefaultsRepoID, edited.Name)
	require.NoError(t, err)
	assert.Equal(t, PolicyOthersOnly, after.ApprovalPolicy, "an edited row is not overwritten")
	assert.EqualValues(t, 2, after.RequiredApprovals)
	assert.EqualValues(t, 999, after.SortOrder)

	// Deleting a seeded row and restarting restores it.
	_, err = db.GetEngine(ctx).ID(first[0].ID).Delete(new(Environment))
	require.NoError(t, err)
	require.Len(t, defaults(t), len(configuredSet)-1)

	require.NoError(t, Seed(ctx, configuredSet))
	assert.Len(t, defaults(t), len(configuredSet))
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
