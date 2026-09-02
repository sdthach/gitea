// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"

	"gitea.dev/services/context"
	delivery_service "gitea.dev/services/delivery"
	"gitea.dev/services/delivery/query"
)

// workflowSpec is the per-workflow statistics resource's whitelist declaration.
//
// Like the grid, these rows are a PROJECTION rather than a table: run counts, success rate
// and average duration are computed in process over the window's runs, because bucketing and
// averaging in SQL would need constructs SQLite and PostgreSQL spell differently.
// Filtering and sorting therefore select what to project instead of rendering into a SQL
// condition — but they still go through the one grammar, so an unknown field or an
// unsortable one is rejected by the same parser every other resource uses.
var workflowSpec = query.Spec{
	Resource: "workflows",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "repo_full_name", Column: "repo_full_name", Kind: query.KindString},
		{Name: "workflow_id", Column: "workflow_id", Kind: query.KindString},
		{Name: "runs", Column: "runs", Kind: query.KindInt},
		{Name: "average_duration_seconds", Column: "average_duration_seconds", Kind: query.KindInt},
		{Name: "disabled", Column: "disabled", Kind: query.KindBool},
		// window_days selects the period the rows are computed over rather than narrowing
		// them. It is a field so the grammar validates it; every other filter here narrows.
		{Name: "window_days", Column: "window_days", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
	},
	SortFields:   []string{"repo_id", "workflow_id", "runs", "average_duration_seconds"},
	DefaultSort:  "runs",
	DefaultOrder: query.OrderDesc,
	PrimaryKey:   "repo_id",
	SearchFields: []string{"repo_full_name", "workflow_id"},
	Paging:       query.PagingOffset,
}

// workflowStatValue reads one field of a projected workflow row, so the grammar's filters are
// applied in process to a projection the same way they would be applied in SQL to a table.
func workflowStatValue(row delivery_service.WorkflowStat, field string) (any, bool) {
	switch field {
	case "repo_id":
		return row.RepoID, true
	case "repo_full_name":
		return row.RepoFullName, true
	case "workflow_id":
		return row.WorkflowID, true
	case "runs":
		return row.Runs, true
	case "average_duration_seconds":
		return row.AverageDurationSeconds, true
	case "disabled":
		return row.Disabled, true
	}
	return nil, false
}

func listWorkflowsEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "listWorkflows", Method: http.MethodGet, Path: "/workflows",
			Summary: "List workflows with their run counts, success rate and average duration",
			Description: "One row per (repository, workflow file) over the selected window, so every figure the " +
				"overview's tiles aggregate is independently queryable rather than reachable only through the " +
				"composite. window_days selects the window and defaults to " +
				"7 days. Rows carry disabled, read from Gitea's own Actions unit configuration. " +
				"Scoped by Gitea's own permission filtering on the Actions unit.",
			Tag: "workflows", Query: &workflowSpec, Response: "WorkflowStat", ResponseIs: "array",
		},
		Handler: ListWorkflows,
	}
}

// ListWorkflows answers GET /workflows.
func ListWorkflows(ctx *context.APIContext) {
	q, ok := parseQuery(ctx, workflowSpec)
	if !ok {
		return
	}
	opts, ok := overviewOptions(ctx, q)
	if !ok {
		return
	}

	rows, _, err := delivery_service.BuildWorkflowStats(ctx, opts)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	rows = filterProjection(rows, q, workflowStatValue)
	delivery_service.SortWorkflowStats(rows, q.Sort.Column, q.Sort.Order)
	renderPage(ctx, q, int64(len(rows)), pageOf(rows, q))
}

// pageOf applies the requested page to a projection. The rows are computed in process, so
// the slice is taken here rather than in SQL; the sort is tie-broken first, so the same page
// asked for twice holds the same rows.
func pageOf[T any](rows []T, q *query.Query) []T {
	offset := q.Offset()
	if offset >= len(rows) {
		return []T{}
	}
	end := min(offset+q.Limit, len(rows))
	return rows[offset:end]
}
