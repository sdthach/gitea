// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"testing"
	"time"

	actions_model "gitea.dev/models/actions"
	deployments_model "gitea.dev/models/deployments"
	"gitea.dev/models/unittest"
	"gitea.dev/services/hub/query"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// day is the UTC midnight the fixtures below bucket into.
var day = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC).Unix()

// runAt is one run fact: created at day+offset, with the given status and duration.
func runAt(id, repoID int64, workflow string, status actions_model.Status, offset, duration int64) deployments_model.RunFact {
	created := day + offset
	fact := deployments_model.RunFact{
		ID: id, RepoID: repoID, WorkflowID: workflow, Status: int(status), CreatedUnix: created,
	}
	if duration > 0 {
		fact.StartedUnix = created
		fact.StoppedUnix = created + duration
	}
	return fact
}

// TestDeliveryRunStateOfCoversEveryActionsStatus is the mapping the tiles group on. Every
// upstream status has to land in a named bucket, or a run would vanish from a total without
// anything saying so.
func TestDeliveryRunStateOfCoversEveryActionsStatus(t *testing.T) {
	cases := map[actions_model.Status]RunState{
		actions_model.StatusSuccess:    StateSuccess,
		actions_model.StatusFailure:    StateFailure,
		actions_model.StatusRunning:    StateInProgress,
		actions_model.StatusCancelling: StateInProgress,
		actions_model.StatusWaiting:    StateQueued,
		actions_model.StatusBlocked:    StateQueued,
		actions_model.StatusCancelled:  StateCancelled,
		actions_model.StatusSkipped:    StateSkipped,
		actions_model.StatusUnknown:    StateUnknown,
	}
	for status, want := range cases {
		assert.Equal(t, want, RunStateOf(int(status)), "status %s", status)
	}
	assert.Equal(t, StateUnknown, RunStateOf(9999), "an unrecognised status is named, never dropped")
}

// TestDeliveryRunStatusCodesWidensAMultiStatusState is why the /runs status filter renders as
// an IN: in_progress covers two upstream statuses, and matching only one would be wrong.
func TestDeliveryRunStatusCodesWidensAMultiStatusState(t *testing.T) {
	codes, ok := RunStatusCodes("in_progress")
	require.True(t, ok)
	assert.ElementsMatch(t,
		[]int64{int64(actions_model.StatusRunning), int64(actions_model.StatusCancelling)}, codes)

	codes, ok = RunStatusCodes("failure")
	require.True(t, ok)
	assert.Equal(t, []int64{int64(actions_model.StatusFailure)}, codes)

	_, ok = RunStatusCodes("exploded")
	assert.False(t, ok, "an unknown state name is refused, not silently matched against nothing")
}

func TestDeliveryWindowIsHalfOpenAndItsPredecessorAdjoinsIt(t *testing.T) {
	now := time.Unix(day+86400*3, 0)
	w := NewWindow(7, now)
	assert.Equal(t, int64(7*86400), w.ToUnix-w.FromUnix)

	previous := w.Previous()
	assert.Equal(t, w.FromUnix, previous.ToUnix, "the previous window ends exactly where this one begins")
	assert.Equal(t, w.ToUnix-w.FromUnix, previous.ToUnix-previous.FromUnix,
		"the comparison window is of equal length")

	assert.Equal(t, DefaultWindowDays, NewWindow(0, now).Days, "no window asked for is the default window")
	assert.Equal(t, MaxWindowDays, NewWindow(100000, now).Days, "an absurd window is clamped, not refused")
}

// TestDeliverySummarizeRunsCountsEveryState covers the tile set over a fixture run set.
func TestDeliverySummarizeRunsCountsEveryState(t *testing.T) {
	facts := []deployments_model.RunFact{
		runAt(1, 10, "ci.yaml", actions_model.StatusSuccess, 10, 30),
		runAt(2, 10, "ci.yaml", actions_model.StatusSuccess, 20, 50),
		runAt(3, 10, "release.yaml", actions_model.StatusFailure, 30, 10),
		runAt(4, 11, "ci.yaml", actions_model.StatusRunning, 40, 0),
		runAt(5, 11, "ci.yaml", actions_model.StatusWaiting, 50, 0),
		runAt(6, 11, "ci.yaml", actions_model.StatusCancelled, 60, 5),
	}
	// Three repositories are visible; only two of them ran anything.
	s := SummarizeRuns(facts, NewWindow(7, time.Unix(day+86400, 0)), 3, map[int64][]string{10: {"old.yaml"}})

	assert.Equal(t, int64(6), s.TotalRuns)
	assert.Equal(t, int64(2), s.Runs["success"])
	assert.Equal(t, int64(1), s.Runs["failure"])
	assert.Equal(t, int64(1), s.Runs["in_progress"])
	assert.Equal(t, int64(1), s.Runs["queued"])
	assert.Equal(t, int64(1), s.Runs["cancelled"])

	assert.InDelta(t, 2.0/3.0, s.SuccessRate, 1e-9,
		"the rate is successes over runs that reached a result; a queue is not a quality regression")
	assert.Equal(t, int64(95), s.TotalDurationSeconds)

	assert.Equal(t, int64(2), s.ActiveRepositories)
	assert.Equal(t, int64(1), s.InactiveRepositories)
	assert.Equal(t, int64(3), s.ActiveWorkflows, "two files in repo 10, one in repo 11")
	assert.Equal(t, int64(1), s.DisabledWorkflows)
}

func TestDeliverySummarizeRunsOnAnEmptyWindow(t *testing.T) {
	s := SummarizeRuns(nil, NewWindow(7, time.Unix(day, 0)), 4, nil)
	assert.Equal(t, int64(0), s.TotalRuns)
	assert.InDelta(t, 0.0, s.SuccessRate, 1e-9, "no run is not a zero success rate to panic about")
	assert.Equal(t, int64(0), s.ActiveRepositories)
	assert.Equal(t, int64(4), s.InactiveRepositories)
	for _, state := range RunStates {
		assert.Contains(t, s.Runs, state, "every state is present as an explicit zero, so no tile is missing")
	}
}

func TestDeliveryAggregateReposRanksByVolumeAndCarriesTheRate(t *testing.T) {
	facts := []deployments_model.RunFact{
		runAt(1, 10, "ci.yaml", actions_model.StatusSuccess, 10, 30),
		runAt(2, 10, "ci.yaml", actions_model.StatusFailure, 20, 50),
		runAt(3, 10, "ci.yaml", actions_model.StatusSuccess, 30, 10),
		runAt(4, 11, "ci.yaml", actions_model.StatusSuccess, 40, 100),
	}
	rows := AggregateRepos(facts, map[int64]string{10: "acme/web", 11: "acme/api"})
	require.Len(t, rows, 2)

	assert.Equal(t, "acme/web", rows[0].RepoFullName, "top repositories are by run volume")
	assert.Equal(t, int64(3), rows[0].Runs)
	assert.InDelta(t, 2.0/3.0, rows[0].SuccessRate, 1e-9)
	assert.Equal(t, int64(30), rows[0].AverageDurationSeconds, "(30+50+10)/3")

	assert.Equal(t, "acme/api", rows[1].RepoFullName)
	assert.Equal(t, int64(100), rows[1].AverageDurationSeconds)

	SortRepoStats(rows, "repo_full_name", "asc")
	assert.Equal(t, "acme/api", rows[0].RepoFullName, "the resource's sort is honoured, not only the default")
}

func TestDeliveryAggregateWorkflowsMarksTheDisabledOnes(t *testing.T) {
	facts := []deployments_model.RunFact{
		runAt(1, 10, "ci.yaml", actions_model.StatusSuccess, 10, 30),
		runAt(2, 10, "ci.yaml", actions_model.StatusFailure, 20, 30),
		runAt(3, 10, "legacy.yaml", actions_model.StatusSuccess, 30, 60),
	}
	rows := AggregateWorkflows(facts, map[int64]string{10: "acme/web"}, map[int64][]string{10: {"legacy.yaml"}})
	require.Len(t, rows, 2)

	assert.Equal(t, "ci.yaml", rows[0].WorkflowID)
	assert.Equal(t, int64(2), rows[0].Runs)
	assert.False(t, rows[0].Disabled)

	assert.Equal(t, "legacy.yaml", rows[1].WorkflowID)
	assert.True(t, rows[1].Disabled,
		"a disabled workflow that still ran in the window is shown as disabled, never hidden — hiding it would lose its run from the total")
}

// TestDeliveryDailyTrendIncludesQuietDaysAndDeployments: the deployment count comes
// from the fork's own table, so this dashboard and the delivery grid cannot disagree.
func TestDeliveryDailyTrendIncludesQuietDaysAndDeployments(t *testing.T) {
	window := Window{FromUnix: day, ToUnix: day + 2*86400, Days: 2}
	facts := []deployments_model.RunFact{
		runAt(1, 10, "ci.yaml", actions_model.StatusSuccess, 3600, 30),
		runAt(2, 10, "ci.yaml", actions_model.StatusFailure, 7200, 90),
		// nothing on day+1
		runAt(3, 10, "ci.yaml", actions_model.StatusSuccess, 2*86400+60, 10),
	}
	deployments := []deployments_model.DeploymentFact{
		{ID: 1, RepoID: 10, CreatedUnix: day + 3700},
		{ID: 2, RepoID: 10, CreatedUnix: day + 2*86400 + 90},
	}

	points := DailyTrend(facts, deployments, window)
	require.Len(t, points, 3, "one point per UTC day across the window, both ends included")

	assert.Equal(t, "2026-03-02", points[0].Date)
	assert.Equal(t, int64(2), points[0].Runs)
	assert.Equal(t, int64(1), points[0].Successes)
	assert.Equal(t, int64(1), points[0].Failures)
	assert.Equal(t, int64(60), points[0].AverageDurationSeconds)
	assert.Equal(t, int64(1), points[0].Deployments)

	assert.Equal(t, "2026-03-03", points[1].Date)
	assert.Equal(t, int64(0), points[1].Runs, "a quiet day is a zero, never a gap that reads as missing data")
	assert.Equal(t, int64(0), points[1].Deployments)

	assert.Equal(t, "2026-03-04", points[2].Date)
	assert.Equal(t, int64(1), points[2].Runs)
	assert.Equal(t, int64(1), points[2].Deployments)
}

func TestDeliveryDailyTrendBucketsPreEpochTimestampsIntoTheirOwnDay(t *testing.T) {
	// A floored modulo is what keeps a negative timestamp in the day it belongs to. A Go
	// remainder would put it in the following one.
	assert.Equal(t, int64(-86400), dayStart(-1))
	assert.Equal(t, int64(0), dayStart(0))
	assert.Equal(t, int64(0), dayStart(86399))
}

// TestDeliveryOverviewOptionsScopeIsFailClosed is the security branch. A filter that widened
// instead of narrowing here would leak every repository's runs to any caller.
func TestDeliveryOverviewOptionsScopeIsFailClosed(t *testing.T) {
	assert.Nil(t, OverviewOptions{RepoIDs: nil}.scope(),
		"no accessible repository aggregates nothing, never everything")
	assert.Equal(t, []int64{1, 2}, OverviewOptions{RepoIDs: []int64{1, 2}}.scope())
	assert.Equal(t, []int64{2}, OverviewOptions{RepoIDs: []int64{1, 2}, RepoID: 2}.scope())
	assert.Nil(t, OverviewOptions{RepoIDs: []int64{1, 2}, RepoID: 99}.scope(),
		"asking for a repository outside the accessible set narrows to nothing, it does not widen")
}

// TestDeliveryBuildOverviewExcludesARepositoryTheViewerCannotSee exercises the exported entry
// point the /overview handler calls — not the projection underneath it — so a wrapper that
// dropped the scope could not pass.
func TestDeliveryBuildOverviewExcludesARepositoryTheViewerCannotSee(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	// Both repositories carry runs in the shared fixtures, created at 1683636108.
	const publicRepo, privateRepo = int64(4), int64(2)
	window := Window{FromUnix: 0, ToUnix: 1 << 40, Days: 1}

	both, err := BuildOverview(ctx, OverviewOptions{RepoIDs: []int64{publicRepo, privateRepo}, Window: window})
	require.NoError(t, err)
	require.Positive(t, both.Summary.TotalRuns)

	onlyPublic, err := BuildOverview(ctx, OverviewOptions{RepoIDs: []int64{publicRepo}, Window: window})
	require.NoError(t, err)
	assert.Less(t, onlyPublic.Summary.TotalRuns, both.Summary.TotalRuns,
		"a run in a repository outside the accessible set must not be counted")
	assert.Equal(t, int64(1), onlyPublic.Summary.ActiveRepositories)

	none, err := BuildOverview(ctx, OverviewOptions{RepoIDs: nil, Window: window})
	require.NoError(t, err)
	assert.Equal(t, int64(0), none.Summary.TotalRuns)
	assert.Equal(t, int64(0), none.Summary.InactiveRepositories)
}

// TestDeliveryBuildOverviewCountsMatchThePerRepositoryQuery: the aggregate has to
// agree with the same question asked one repository at a time.
func TestDeliveryBuildOverviewCountsMatchThePerRepositoryQuery(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	repoIDs := []int64{2, 4, 5}
	window := Window{FromUnix: 0, ToUnix: 1 << 40, Days: 1}

	aggregate, err := BuildOverview(ctx, OverviewOptions{RepoIDs: repoIDs, Window: window})
	require.NoError(t, err)

	var perRepo int64
	perRepoStates := map[string]int64{}
	for _, id := range repoIDs {
		one, err := BuildOverview(ctx, OverviewOptions{RepoIDs: repoIDs, RepoID: id, Window: window})
		require.NoError(t, err)
		perRepo += one.Summary.TotalRuns
		for state, n := range one.Summary.Runs {
			perRepoStates[state] += n
		}
	}
	assert.Equal(t, aggregate.Summary.TotalRuns, perRepo,
		"the cross-repository total is the sum of the same query run per repository")
	for state, n := range perRepoStates {
		assert.Equal(t, aggregate.Summary.Runs[state], n, "state %q", state)
	}
}

// TestDeliveryBuildTrendsMatchesTheDeliveryTables is the deployment half: the trend's
// deployment count is the fork's own table, read back.
func TestDeliveryBuildTrendsMatchesTheDeliveryTables(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	require.NoError(t, deployments_model.AppendDeployment(ctx, &deployments_model.Deployment{
		RepoID: 4, Environment: "qa", ReleaseTag: "v1", RunID: 9001, Status: "success",
	}))
	require.NoError(t, deployments_model.AppendDeployment(ctx, &deployments_model.Deployment{
		RepoID: 4, Environment: "prod", ReleaseTag: "v1", RunID: 9002, Status: "success",
	}))

	window := NewWindow(7, time.Now())
	points, _, err := BuildTrends(ctx, OverviewOptions{RepoIDs: []int64{4}, Window: window})
	require.NoError(t, err)

	var deployments int64
	for _, p := range points {
		deployments += p.Deployments
	}
	rows, err := deployments_model.FindDeploymentFacts(ctx, []int64{4}, window.FromUnix, window.ToUnix)
	require.NoError(t, err)
	assert.Equal(t, int64(len(rows)), deployments,
		"the trend's deployment count is the delivery table, so both dashboards share one source of truth")
	assert.Equal(t, int64(2), deployments)

	elsewhere, _, err := BuildTrends(ctx, OverviewOptions{RepoIDs: []int64{2}, Window: window})
	require.NoError(t, err)
	var leaked int64
	for _, p := range elsewhere {
		leaked += p.Deployments
	}
	assert.Equal(t, int64(0), leaked, "a deployment outside the accessible set reaches no trend")
}

// TestDeliveryBuildRepoStatsAndWorkflowStatsAreScoped exercises the two remaining exported
// entry points the handlers call.
func TestDeliveryBuildRepoStatsAndWorkflowStatsAreScoped(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	window := Window{FromUnix: 0, ToUnix: 1 << 40, Days: 1}

	repos, _, err := BuildRepoStats(ctx, OverviewOptions{RepoIDs: []int64{4}, Window: window})
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, int64(4), repos[0].RepoID)
	assert.NotEmpty(t, repos[0].RepoFullName, "the row carries the name it links out to Gitea by")
	assert.Positive(t, repos[0].Runs)

	workflows, _, err := BuildWorkflowStats(ctx, OverviewOptions{RepoIDs: []int64{4}, Window: window})
	require.NoError(t, err)
	require.NotEmpty(t, workflows)
	for _, w := range workflows {
		assert.Equal(t, int64(4), w.RepoID)
	}

	empty, _, err := BuildRepoStats(ctx, OverviewOptions{RepoIDs: nil, Window: window})
	require.NoError(t, err)
	assert.Empty(t, empty)

	noWorkflows, _, err := BuildWorkflowStats(ctx, OverviewOptions{RepoIDs: nil, Window: window})
	require.NoError(t, err)
	assert.Empty(t, noWorkflows)
}

// TestDeliverySortWorkflowStatsOrdersAndTieBreaks covers the sort on its own, with an input
// deliberately out of order. Asserting it only through AggregateWorkflows would not do: with
// the sort removed the rows come back in map order, which is random rather than reliably
// wrong, so that assertion could pass by luck.
func TestDeliverySortWorkflowStatsOrdersAndTieBreaks(t *testing.T) {
	rows := []WorkflowStat{
		{RepoID: 2, WorkflowID: "b.yaml", Runs: 1, AverageDurationSeconds: 90},
		{RepoID: 1, WorkflowID: "a.yaml", Runs: 5, AverageDurationSeconds: 10},
		{RepoID: 1, WorkflowID: "c.yaml", Runs: 1, AverageDurationSeconds: 50},
	}

	SortWorkflowStats(rows, "runs", query.OrderDesc)
	assert.Equal(t, []string{"a.yaml", "c.yaml", "b.yaml"}, workflowOrder(rows),
		"volume first; the two one-run rows tie and are broken on repo_id ascending, so the same page asked for twice holds the same rows")

	SortWorkflowStats(rows, "runs", query.OrderAsc)
	assert.Equal(t, []string{"c.yaml", "b.yaml", "a.yaml"}, workflowOrder(rows))

	SortWorkflowStats(rows, "workflow_id", query.OrderAsc)
	assert.Equal(t, []string{"a.yaml", "b.yaml", "c.yaml"}, workflowOrder(rows))

	SortWorkflowStats(rows, "average_duration_seconds", query.OrderDesc)
	assert.Equal(t, []string{"b.yaml", "c.yaml", "a.yaml"}, workflowOrder(rows))
}

func workflowOrder(rows []WorkflowStat) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.WorkflowID)
	}
	return out
}

// TestDeliverySortRepoStatsOrdersAndTieBreaks is the same for the repository rows.
func TestDeliverySortRepoStatsOrdersAndTieBreaks(t *testing.T) {
	rows := []RepoStat{
		{RepoID: 3, RepoFullName: "c/three", Runs: 1},
		{RepoID: 1, RepoFullName: "a/one", Runs: 9},
		{RepoID: 2, RepoFullName: "b/two", Runs: 1},
	}

	SortRepoStats(rows, "runs", query.OrderDesc)
	assert.Equal(t, []int64{1, 2, 3}, repoOrder(rows), "the tie between the two 1-run rows is broken on repo_id")

	SortRepoStats(rows, "repo_full_name", query.OrderDesc)
	assert.Equal(t, []int64{3, 2, 1}, repoOrder(rows))
}

func repoOrder(rows []RepoStat) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.RepoID)
	}
	return out
}
