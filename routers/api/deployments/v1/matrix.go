// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"

	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	deployments_service "gitea.dev/services/deployments"
	"gitea.dev/services/hub/query"
)

// matrixSpec is the deployment matrix projection's whitelist declaration.
//
// The matrix is a PROJECTION over the append-only log, not a table, so its filters select
// what to project rather than rendering into a SQL condition. They still go through the one
// grammar, so an unknown field is rejected by the same parser every other resource uses.
// Its rows are releases, which are finite and stable, so it pages by page.
var matrixSpec = query.Spec{
	Resource: "matrix",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "release_tag", Column: "release_tag", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
		{Name: "environment", Column: "environment", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
	},
	PrimaryKey: "id",
	Paging:     query.PagingOffset,
}

func getDeploymentMatrixEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getDeploymentMatrix", Method: http.MethodGet, Path: "/deployments/matrix",
			Summary: "The release × environment deployment matrix",
			Description: "Releases are rows, environments are columns in configured order — sequence is configuration, " +
				"since nothing in Gitea expresses it. Every cell state is a projection over the append-only " +
				"audit log; no row carries a mutable status column the matrix reads. " +
				"The matrix spans the repositories the viewer can see, using Gitea's existing permission " +
				"filtering. The /deployments page is a client of this endpoint.",
			Tag: "matrix", Query: &matrixSpec, Response: "MatrixRow", ResponseIs: "array",
		},
		Handler: GetDeploymentMatrix,
	}
}

// GetDeploymentMatrix answers GET /deployments/matrix.
func GetDeploymentMatrix(ctx *context.APIContext) {
	q, ok := hubapi.ParseQuery(ctx, matrixSpec)
	if !ok {
		return
	}
	repoIDs, ok := accessibleRepoIDs(ctx)
	if !ok {
		return
	}

	rows, total, err := deployments_service.BuildMatrix(ctx, deployments_service.MatrixOptions{
		RepoIDs:     repoIDs,
		RepoID:      hubapi.EqualityFilterInt(q, "repo_id"),
		ReleaseTag:  hubapi.EqualityFilterString(q, "release_tag"),
		Environment: hubapi.EqualityFilterString(q, "environment"),
		Limit:       q.Limit,
		Offset:      q.Offset(),
	})
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	hubapi.RenderPage(ctx, q, total, rows)
}
