// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/url"
	"testing"

	actions_model "gitea.dev/models/actions"
	delivery_service "gitea.dev/services/delivery"
	"gitea.dev/services/delivery/query"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xorm.io/builder"
)

// parseRunQuery is the /runs handler's own parse step, without a request.
func parseRunQuery(t *testing.T, raw string) *query.Query {
	t.Helper()
	values, err := url.ParseQuery(raw)
	require.NoError(t, err)
	q, qErr := query.Parse(values, runSpec)
	require.Nil(t, qErr)
	return q
}

// TestDeliveryRunStatusFilterBecomesTheStoredInteger is what makes SC 41's
// `status[eq]=failure` return the failed runs rather than nothing: the column stores Gitea's
// integer, and the caller filters on the state name the overview publishes.
func TestDeliveryRunStatusFilterBecomesTheStoredInteger(t *testing.T) {
	q := parseRunQuery(t, "status[eq]=failure")
	require.Nil(t, mapRunStatusFilters(q))

	require.Len(t, q.Filters, 1)
	assert.Equal(t, query.OpIn, q.Filters[0].Op)
	assert.Equal(t, []any{int64(actions_model.StatusFailure)}, q.Filters[0].Values)

	sql, args, err := builder.ToSQL(q.Cond())
	require.NoError(t, err)
	assert.Contains(t, sql, "status")
	assert.Contains(t, args, int64(actions_model.StatusFailure))
	assert.NotContains(t, args, "failure", "the name must never reach the statement as a value")
}

// TestDeliveryRunStatusFilterWidensAMultiStatusState: in_progress covers running and
// cancelling, so equality has to widen into a set rather than silently match one of them.
func TestDeliveryRunStatusFilterWidensAMultiStatusState(t *testing.T) {
	q := parseRunQuery(t, "status=in_progress")
	require.Nil(t, mapRunStatusFilters(q))

	require.Len(t, q.Filters, 1)
	assert.Equal(t, query.OpIn, q.Filters[0].Op)
	assert.ElementsMatch(t,
		[]any{int64(actions_model.StatusRunning), int64(actions_model.StatusCancelling)},
		q.Filters[0].Values)
}

// TestDeliveryRunStatusFilterRefusesAnUnknownState is I4/A21: the rejection names the
// offender and lists what is accepted, rather than returning an empty page a caller would
// read as "no failed runs".
func TestDeliveryRunStatusFilterRefusesAnUnknownState(t *testing.T) {
	q := parseRunQuery(t, "status[eq]=exploded")
	qErr := mapRunStatusFilters(q)
	require.NotNil(t, qErr)

	assert.Equal(t, "unknown_run_status", qErr.Code)
	assert.Contains(t, qErr.Message, "exploded", "the rejection names the offender (I4)")
	assert.NotEmpty(t, qErr.SuggestedAction, "every error carries a suggested next action (A21)")
	assert.Contains(t, qErr.Accepted, "failure", "the rejection lists what is accepted (I4)")
}

// TestDeliveryRunStatusFilterLeavesOtherFiltersAlone catches a rewrite that reached past its
// own field.
func TestDeliveryRunStatusFilterLeavesOtherFiltersAlone(t *testing.T) {
	q := parseRunQuery(t, "workflow_id=ci.yaml&repo_id=7")
	require.Nil(t, mapRunStatusFilters(q))

	for _, f := range q.Filters {
		switch f.Field.Name {
		case "workflow_id":
			assert.Equal(t, []any{"ci.yaml"}, f.Values)
		case "repo_id":
			assert.Equal(t, []any{int64(7)}, f.Values)
		default:
			t.Fatalf("unexpected filter %q", f.Field.Name)
		}
	}
}

// TestDeliveryOverviewResourcesArePublished is P8/P9: every figure the composite shows is
// also reachable from a resource of its own, so the composite is a saving rather than the
// only door.
func TestDeliveryOverviewResourcesArePublished(t *testing.T) {
	published := map[string]bool{}
	for _, op := range Operations() {
		published[op.Path] = true
	}
	for _, path := range []string{"/runs", "/workflows", "/overview", "/overview/trends", "/overview/repos"} {
		assert.True(t, published[path], "%s must be a published operation (P8, P9)", path)
	}
}

// TestDeliveryProjectionFiltersNarrowTheRows is what keeps a declared filter from being a
// silent no-op. The projections are computed in process, so their conditions cannot be pushed
// into SQL; every field the resource publishes as filterable has to actually narrow.
func TestDeliveryProjectionFiltersNarrowTheRows(t *testing.T) {
	rows := []delivery_service.RepoStat{
		{RepoID: 1, RepoFullName: "acme/web", Runs: 10, AverageDurationSeconds: 30},
		{RepoID: 2, RepoFullName: "acme/api", Runs: 3, AverageDurationSeconds: 200},
		{RepoID: 3, RepoFullName: "other/tool", Runs: 7, AverageDurationSeconds: 60},
	}
	apply := func(raw string) []delivery_service.RepoStat {
		values, err := url.ParseQuery(raw)
		require.NoError(t, err)
		q, qErr := query.Parse(values, overviewRepoSpec)
		require.Nil(t, qErr)
		return filterProjection(rows, q, repoStatValue)
	}

	assert.Len(t, apply("runs[gte]=7"), 2)
	assert.Len(t, apply("runs[lt]=7"), 1)
	assert.Len(t, apply("repo_id=2"), 1)
	assert.Len(t, apply("repo_full_name[contains]=acme"), 2)
	assert.Len(t, apply("repo_full_name[in]=acme/web,other/tool"), 2)
	assert.Len(t, apply("repo_full_name[ne]=acme/web"), 2)
	assert.Len(t, apply("q=OTHER"), 1, "free-text search is case-insensitive over the declared columns (I10)")
	assert.Len(t, apply("runs[gte]=7&repo_full_name[contains]=acme"), 1, "repeating narrows, it does not widen")
	assert.Len(t, apply("window_days=30"), 3, "window_days selects the period; it narrows no row")
	assert.Len(t, apply(""), 3)
}

func TestDeliveryProjectionFiltersOnABooleanField(t *testing.T) {
	rows := []delivery_service.WorkflowStat{
		{RepoID: 1, WorkflowID: "ci.yaml", Runs: 4},
		{RepoID: 1, WorkflowID: "legacy.yaml", Runs: 1, Disabled: true},
	}
	values, err := url.ParseQuery("disabled=true")
	require.NoError(t, err)
	q, qErr := query.Parse(values, workflowSpec)
	require.Nil(t, qErr)

	kept := filterProjection(rows, q, workflowStatValue)
	require.Len(t, kept, 1)
	assert.Equal(t, "legacy.yaml", kept[0].WorkflowID)
}

// TestDeliveryEveryDeclaredProjectionFieldIsReadable catches the defect the filter helper is
// designed around: a field published as filterable that the row accessor cannot read would
// match every row silently.
func TestDeliveryEveryDeclaredProjectionFieldIsReadable(t *testing.T) {
	// window_days selects the window rather than narrowing rows, which is why it is the one
	// declared field with no accessor.
	const windowField = "window_days"

	for _, f := range overviewRepoSpec.Fields {
		if f.Name == windowField {
			continue
		}
		_, readable := repoStatValue(delivery_service.RepoStat{}, f.Name)
		assert.True(t, readable, "overview-repos publishes %q as filterable but cannot read it", f.Name)
	}
	for _, f := range workflowSpec.Fields {
		if f.Name == windowField {
			continue
		}
		_, readable := workflowStatValue(delivery_service.WorkflowStat{}, f.Name)
		assert.True(t, readable, "workflows publishes %q as filterable but cannot read it", f.Name)
	}
	for _, col := range append(overviewRepoSpec.SearchFields, workflowSpec.SearchFields...) {
		_, readableRepo := repoStatValue(delivery_service.RepoStat{}, col)
		_, readableWorkflow := workflowStatValue(delivery_service.WorkflowStat{}, col)
		assert.True(t, readableRepo || readableWorkflow, "a searched column %q must be readable", col)
	}
}

// TestDeliveryOverviewPagesAProjection covers the in-process paging the projections use,
// including the past-the-end page a naive slice would panic on.
func TestDeliveryOverviewPagesAProjection(t *testing.T) {
	rows := []int{1, 2, 3, 4, 5}
	page := func(p, limit int) []int {
		values, err := url.ParseQuery("")
		require.NoError(t, err)
		q, qErr := query.Parse(values, overviewRepoSpec)
		require.Nil(t, qErr)
		q.Page, q.Limit = p, limit
		return pageOf(rows, q)
	}
	assert.Equal(t, []int{1, 2}, page(1, 2))
	assert.Equal(t, []int{3, 4}, page(2, 2))
	assert.Equal(t, []int{5}, page(3, 2))
	assert.Empty(t, page(4, 2), "a page past the end is empty, not a panic")
}
