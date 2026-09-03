// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"testing"

	"gitea.dev/models/db"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Repository 4 is public and carries four runs in the shared fixtures; repository 2 is
// private to user2 and carries two. That pair is what makes the scoping tests below able to
// fail in both directions.
const (
	fixturePublicRepoID  = 4
	fixturePrivateRepoID = 2
)

func TestDeliveryRunFactDurationIgnoresUnfinishedAndInvertedRuns(t *testing.T) {
	cases := map[string]struct {
		fact RunFact
		want int64
	}{
		"finished":             {RunFact{StartedUnix: 100, StoppedUnix: 160}, 60},
		"never started":        {RunFact{StartedUnix: 0, StoppedUnix: 160}, 0},
		"still running":        {RunFact{StartedUnix: 100, StoppedUnix: 0}, 0},
		"stopped before start": {RunFact{StartedUnix: 200, StoppedUnix: 100}, 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, c.fact.DurationSeconds(),
				"a negative duration would silently reduce a total rather than being visible as one bad row")
		})
	}
}

// TestDeliveryFindRunFactsIsScopedToTheGivenRepositories is the permission filter's data
// half, in both its including and its excluding case. The handler resolves the
// accessible set through Gitea's own check; this proves the read honours it.
func TestDeliveryFindRunFactsIsScopedToTheGivenRepositories(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	// The fixtures' runs were created at 1683636108; a window wide enough to hold them.
	const from, to = int64(0), int64(1 << 40)

	included, _, err := FindRunFacts(ctx, []int64{fixturePublicRepoID}, from, to)
	require.NoError(t, err)
	require.NotEmpty(t, included, "the including case has to see rows, or the excluding case proves nothing")
	for _, f := range included {
		assert.Equal(t, int64(fixturePublicRepoID), f.RepoID)
	}

	both, _, err := FindRunFacts(ctx, []int64{fixturePublicRepoID, fixturePrivateRepoID}, from, to)
	require.NoError(t, err)
	assert.Greater(t, len(both), len(included),
		"the private repository's runs exist, so leaving it out of the scope is what excluded them")

	excluded, _, err := FindRunFacts(ctx, []int64{fixturePublicRepoID}, from, to)
	require.NoError(t, err)
	for _, f := range excluded {
		assert.NotEqual(t, int64(fixturePrivateRepoID), f.RepoID,
			"a run in a repository outside the scope must not appear")
	}
}

// TestDeliveryFindRunFactsIsFailClosedOnAnEmptyScope is the branch a fail-OPEN filter would
// get wrong: "no accessible repository" must aggregate nothing, never everything.
func TestDeliveryFindRunFactsIsFailClosedOnAnEmptyScope(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	facts, truncated, err := FindRunFacts(t.Context(), nil, 0, 1<<40)
	require.NoError(t, err)
	assert.Empty(t, facts, "an empty accessible set is an empty aggregate, not an unscoped one")
	assert.False(t, truncated)

	deployments, err := FindDeploymentFacts(t.Context(), nil, 0, 1<<40)
	require.NoError(t, err)
	assert.Empty(t, deployments)

	disabled, err := FindDisabledWorkflows(t.Context(), nil)
	require.NoError(t, err)
	assert.Empty(t, disabled)
}

// TestDeliveryFindRunFactsHonoursTheWindow proves the window is half-open, so a run on the
// boundary is counted by exactly one of a window and its predecessor rather than by both.
func TestDeliveryFindRunFactsHonoursTheWindow(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	all, _, err := FindRunFacts(ctx, []int64{fixturePublicRepoID}, 0, 1<<40)
	require.NoError(t, err)
	require.NotEmpty(t, all)
	created := all[0].CreatedUnix

	inside, _, err := FindRunFacts(ctx, []int64{fixturePublicRepoID}, created, created+1)
	require.NoError(t, err)
	assert.NotEmpty(t, inside, "the lower bound is inclusive")

	after, _, err := FindRunFacts(ctx, []int64{fixturePublicRepoID}, created+1, created+2)
	require.NoError(t, err)
	assert.Empty(t, after)

	before, _, err := FindRunFacts(ctx, []int64{fixturePublicRepoID}, created-1, created)
	require.NoError(t, err)
	assert.Empty(t, before, "the upper bound is exclusive, so no run is counted by two adjacent windows")
}

// TestDeliveryFindDeploymentFactsReadsTheForksOwnTable: the daily trend's deployment count
// comes from delivery_deployment, not from counting runs.
func TestDeliveryFindDeploymentFactsReadsTheForksOwnTable(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	require.NoError(t, AppendDeployment(ctx, &Deployment{
		RepoID: fixturePublicRepoID, Environment: "qa", ReleaseTag: "v1",
		RunID: 4242, Status: "success",
	}))

	facts, err := FindDeploymentFacts(ctx, []int64{fixturePublicRepoID}, 0, 1<<40)
	require.NoError(t, err)
	require.Len(t, facts, 1)
	assert.Equal(t, int64(fixturePublicRepoID), facts[0].RepoID)

	elsewhere, err := FindDeploymentFacts(ctx, []int64{fixturePrivateRepoID}, 0, 1<<40)
	require.NoError(t, err)
	assert.Empty(t, elsewhere, "a deployment outside the scope must not reach the trend")
}

// TestDeliveryFindRunsAppliesTheGrammarsCondition covers the /runs read path: it takes the
// condition the one grammar rendered rather than growing a second filter implementation.
func TestDeliveryFindRunsAppliesTheGrammarsCondition(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	runs, total, err := FindRuns(ctx, builderEq("repo_id", int64(fixturePublicRepoID)), "id ASC", 2, 0)
	require.NoError(t, err)
	assert.Len(t, runs, 2, "the limit is applied")
	assert.Greater(t, total, int64(2), "the total counts every matching row, not the page")

	page2, _, err := FindRuns(ctx, builderEq("repo_id", int64(fixturePublicRepoID)), "id ASC", 2, 2)
	require.NoError(t, err)
	require.NotEmpty(t, page2)
	assert.NotEqual(t, runs[0].ID, page2[0].ID, "the offset moves the page")
}

// TestDeliveryFindDisabledWorkflowsReadsGiteasOwnUnitConfig proves the "workflows disabled"
// tile counts what Gitea's own repository settings wrote, rather than a mirrored list the
// fork would have to keep in step.
func TestDeliveryFindDisabledWorkflowsReadsGiteasOwnUnitConfig(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	before, err := FindDisabledWorkflows(ctx, []int64{fixturePublicRepoID})
	require.NoError(t, err)
	assert.Empty(t, before[fixturePublicRepoID], "nothing is disabled until Gitea's own settings say so")

	unitRow := unittest.AssertExistsAndLoadBean(t, &repo_model.RepoUnit{
		RepoID: fixturePublicRepoID, Type: unit.TypeActions,
	})
	cfg := unitRow.ActionsConfig()
	cfg.DisableWorkflow("legacy.yaml")
	unitRow.Config = cfg
	_, err = db.GetEngine(ctx).ID(unitRow.ID).Cols("config").Update(unitRow)
	require.NoError(t, err)

	after, err := FindDisabledWorkflows(ctx, []int64{fixturePublicRepoID})
	require.NoError(t, err)
	assert.Equal(t, []string{"legacy.yaml"}, after[fixturePublicRepoID])

	elsewhere, err := FindDisabledWorkflows(ctx, []int64{fixturePrivateRepoID})
	require.NoError(t, err)
	assert.Empty(t, elsewhere[fixturePublicRepoID], "a repository outside the scope contributes nothing")
}
