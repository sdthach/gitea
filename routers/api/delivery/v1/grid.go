// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"

	"gitea.dev/services/context"
	delivery_service "gitea.dev/services/deployments"
	"gitea.dev/services/hub/query"
)

// gridSpec is the grid projection's whitelist declaration.
//
// The grid is a PROJECTION over the append-only log, not a table, so its filters select
// what to project rather than rendering into a SQL condition. They still go through the one
// grammar, so an unknown field is rejected by the same parser every other resource uses.
// Its rows are releases, which are finite and stable, so it pages by page.
var gridSpec = query.Spec{
	Resource: "grid",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "release_tag", Column: "release_tag", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
		{Name: "environment", Column: "environment", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
	},
	PrimaryKey: "id",
	Paging:     query.PagingOffset,
}

func getGridEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "getGrid", Method: http.MethodGet, Path: "/grid",
			Summary: "The release × environment grid",
			Description: "Releases are rows, environments are columns in configured order — sequence is configuration, " +
				"since nothing in Gitea expresses it. Every cell state is a projection over the append-only " +
				"audit log; no row carries a mutable status column the grid reads. " +
				"The grid spans the repositories the viewer can see, using Gitea's existing permission " +
				"filtering. The /delivery/grid page is a client of this endpoint.",
			Tag: "grid", Query: &gridSpec, Response: "GridRow", ResponseIs: "array",
		},
		Handler: GetGrid,
	}
}

// GetGrid answers GET /grid.
func GetGrid(ctx *context.APIContext) {
	q, ok := parseQuery(ctx, gridSpec)
	if !ok {
		return
	}
	repoIDs, ok := accessibleRepoIDs(ctx)
	if !ok {
		return
	}

	rows, total, err := delivery_service.BuildGrid(ctx, delivery_service.GridOptions{
		RepoIDs:     repoIDs,
		RepoID:      equalityFilterInt(q, "repo_id"),
		ReleaseTag:  equalityFilterString(q, "release_tag"),
		Environment: equalityFilterString(q, "environment"),
		Limit:       q.Limit,
		Offset:      q.Offset(),
	})
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	renderPage(ctx, q, total, rows)
}
