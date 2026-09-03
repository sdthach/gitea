// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"
	"strings"
	"time"

	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	deployments_service "gitea.dev/services/deployments"
	"gitea.dev/services/hub/query"
)

// overviewSpec is the composite's whitelist declaration. The composite is not a list, so it
// declares only the two parameters that narrow it — and it declares them through the one
// grammar, so an unknown parameter is a 400 that names the offender rather than a silently
// ignored word.
var overviewSpec = query.Spec{
	Resource: "overview",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "window_days", Column: "window_days", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
	},
	PrimaryKey: "repo_id",
	Paging:     query.PagingOffset,
}

// overviewRepoSpec is the per-repository statistics resource. The `repos`
// resource already publishes repository IDENTITY; this publishes their run STATISTICS over a
// window, which is a different shape with a different lifetime, so it is a resource of its
// own rather than a second meaning for the same rows.
var overviewRepoSpec = query.Spec{
	Resource: "overview-repos",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "repo_full_name", Column: "repo_full_name", Kind: query.KindString},
		{Name: "runs", Column: "runs", Kind: query.KindInt},
		{Name: "average_duration_seconds", Column: "average_duration_seconds", Kind: query.KindInt},
		// window_days selects the period the rows are computed over rather than narrowing
		// them. It is a field so the grammar validates it; every other filter here narrows.
		{Name: "window_days", Column: "window_days", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
	},
	SortFields:   []string{"repo_id", "repo_full_name", "runs", "average_duration_seconds"},
	DefaultSort:  "runs",
	DefaultOrder: query.OrderDesc,
	PrimaryKey:   "repo_id",
	SearchFields: []string{"repo_full_name"},
	Paging:       query.PagingOffset,
}

// repoStatValue reads one field of a projected repository row, so the grammar's filters are
// applied in process to a projection the same way they would be applied in SQL to a table.
func repoStatValue(row deployments_service.RepoStat, field string) (any, bool) {
	switch field {
	case "repo_id":
		return row.RepoID, true
	case "repo_full_name":
		return row.RepoFullName, true
	case "runs":
		return row.Runs, true
	case "average_duration_seconds":
		return row.AverageDurationSeconds, true
	}
	return nil, false
}

func getOverviewEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getOverview", Method: http.MethodGet, Path: "/overview",
			Summary: "The cross-repository CI summary and its comparison window",
			Description: "Repositories active/inactive, workflows active/disabled, and runs by state with success rate " +
				"and total duration, beside the previous window of equal length. " +
				"The composite exists to save round trips, NEVER as the only way to reach the data: every number in " +
				"it is independently queryable from /runs, /workflows and /overview/repos. " +
				"Aggregates are computed in process over Gitea's own action_run, since its Actions API lists runs " +
				"one repository and one workflow at a time and cannot group. " +
				"Scoped by Gitea's own permission filtering on the Actions unit: a run in a repository the viewer " +
				"cannot read appears in no figure. " +
				"The /delivery/ci page is a client of this endpoint.",
			Tag: "overview", Query: &overviewSpec, Response: "Overview", ResponseIs: "object",
		},
		Handler: GetOverview,
	}
}

func getOverviewTrendsEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getOverviewTrends", Method: http.MethodGet, Path: "/overview/trends",
			Summary: "The daily trend series: total, successful and failed runs, average duration and deployments",
			Description: "One point per UTC day across the window, including days with no run — a gap would read as " +
				"missing data rather than as a quiet day. " +
				"The deployment count reads the fork's own delivery_deployment table rather than counting deploy " +
				"runs, so this dashboard and the delivery grid share one source of truth. " +
				"Days are bucketed in process: SQLite spells the truncation strftime and PostgreSQL date_trunc, and " +
				"one schema has to answer both.",
			Tag: "overview", Query: &overviewSpec, Response: "TrendPoint", ResponseIs: "array",
		},
		Handler: GetOverviewTrends,
	}
}

func listOverviewReposEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "listOverviewRepos", Method: http.MethodGet, Path: "/overview/repos",
			Summary: "List repositories by run volume, with success rate and average duration",
			Description: "The top-repositories list, reachable as a queryable resource rather than only as a " +
				"panel on the page. Sorting defaults to run volume descending. " +
				"Each row carries repo_full_name so it links out to Gitea's own repository page; the overview " +
				"duplicates no Gitea page. " +
				"Scoped by Gitea's own permission filtering on the Actions unit.",
			Tag: "overview", Query: &overviewRepoSpec, Response: "RepoStat", ResponseIs: "array",
		},
		Handler: ListOverviewRepos,
	}
}

// overviewOptions resolves the permission scope and the window every overview resource
// shares.
//
// It is the one place the CI overview's permission filter is applied. It is fail-CLOSED: a
// caller who can see no repository aggregates nothing, and a repo_id outside the accessible
// set narrows to nothing rather than widening to every repository.
func overviewOptions(ctx *context.APIContext, q *query.Query) (deployments_service.OverviewOptions, bool) {
	repoIDs, ok := accessibleRepoIDs(ctx)
	if !ok {
		return deployments_service.OverviewOptions{}, false
	}
	return deployments_service.OverviewOptions{
		RepoIDs: repoIDs,
		RepoID:  hubapi.EqualityFilterInt(q, "repo_id"),
		Window:  deployments_service.NewWindow(int(hubapi.EqualityFilterInt(q, "window_days")), time.Now()),
	}, true
}

// GetOverview answers GET /overview.
func GetOverview(ctx *context.APIContext) {
	q, ok := hubapi.ParseQuery(ctx, overviewSpec)
	if !ok {
		return
	}
	opts, ok := overviewOptions(ctx, q)
	if !ok {
		return
	}
	overview, err := deployments_service.BuildOverview(ctx, opts)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusOK, overview)
}

// GetOverviewTrends answers GET /overview/trends.
func GetOverviewTrends(ctx *context.APIContext) {
	q, ok := hubapi.ParseQuery(ctx, overviewSpec)
	if !ok {
		return
	}
	opts, ok := overviewOptions(ctx, q)
	if !ok {
		return
	}
	points, _, err := deployments_service.BuildTrends(ctx, opts)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusOK, points)
}

// ListOverviewRepos answers GET /overview/repos.
func ListOverviewRepos(ctx *context.APIContext) {
	q, ok := hubapi.ParseQuery(ctx, overviewRepoSpec)
	if !ok {
		return
	}
	opts, ok := overviewOptions(ctx, q)
	if !ok {
		return
	}
	rows, _, err := deployments_service.BuildRepoStats(ctx, opts)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	rows = filterProjection(rows, q, repoStatValue)
	deployments_service.SortRepoStats(rows, q.Sort.Column, q.Sort.Order)
	hubapi.RenderPage(ctx, q, int64(len(rows)), pageOf(rows, q))
}

// filterProjection applies the grammar's filters and free-text search to rows computed in
// process. The projections are not tables, so their conditions cannot be pushed into SQL —
// but every field a caller may filter on is still the resource's own declared whitelist, and
// an unknown one was already refused by the one parser.
//
// A declared field that this cannot read is a defect rather than a silent no-op: a filter
// that quietly matches everything is worse than one that is rejected.
func filterProjection[T any](rows []T, q *query.Query, value func(T, string) (any, bool)) []T {
	if len(q.Filters) == 0 && q.Search == "" {
		return rows
	}
	kept := make([]T, 0, len(rows))
	for _, row := range rows {
		if !matchesFilters(row, q, value) {
			continue
		}
		kept = append(kept, row)
	}
	return kept
}

func matchesFilters[T any](row T, q *query.Query, value func(T, string) (any, bool)) bool {
	for _, f := range q.Filters {
		got, readable := value(row, f.Field.Name)
		if !readable {
			// The field selects the window or some other input rather than narrowing rows.
			continue
		}
		if !matchFilter(f, got) {
			return false
		}
	}
	if q.Search == "" {
		return true
	}
	for _, col := range q.Spec.SearchFields {
		got, readable := value(row, col)
		if !readable {
			continue
		}
		if text, isString := got.(string); isString && strings.Contains(strings.ToLower(text), strings.ToLower(q.Search)) {
			return true
		}
	}
	return false
}

// matchFilter is the in-process spelling of one parsed filter, matching what
// services/hub/query renders into SQL for a table-backed resource.
func matchFilter(f query.Filter, got any) bool {
	switch f.Op {
	case query.OpIn:
		for _, want := range f.Values {
			if compareValues(got, want) == 0 {
				return true
			}
		}
		return false
	case query.OpContains:
		text, isString := got.(string)
		return isString && strings.Contains(strings.ToLower(text), strings.ToLower(f.Text))
	}
	if len(f.Values) == 0 {
		return true
	}
	cmp := compareValues(got, f.Values[0])
	switch f.Op {
	case query.OpNe:
		return cmp != 0
	case query.OpLt:
		return cmp < 0
	case query.OpLte:
		return cmp <= 0
	case query.OpGt:
		return cmp > 0
	case query.OpGte:
		return cmp >= 0
	}
	return cmp == 0
}

// compareValues orders two whitelisted values. The grammar parses a value to the kind its
// field declared, so both sides are already the same Go type.
func compareValues(got, want any) int {
	switch a := got.(type) {
	case int64:
		if b, ok := want.(int64); ok {
			switch {
			case a < b:
				return -1
			case a > b:
				return 1
			}
			return 0
		}
	case string:
		if b, ok := want.(string); ok {
			return strings.Compare(a, b)
		}
	case bool:
		if b, ok := want.(bool); ok && a == b {
			return 0
		}
		return 1
	}
	return 1
}
