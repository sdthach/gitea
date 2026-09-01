// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"

	"gitea.dev/models/delivery"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/services/context"
	"gitea.dev/services/delivery/query"

	"xorm.io/builder"
)

// deploymentSpec is the deployments resource's whitelist declaration. The table is
// append-only, so it pages by cursor: an offset traversal over a table receiving concurrent
// inserts returns rows twice and misses others (I6, I8).
var deploymentSpec = query.Spec{
	Resource: "deployments",
	Fields: []query.Field{
		{Name: "id", Column: "id", Kind: query.KindInt},
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt},
		{Name: "environment", Column: "environment", Kind: query.KindString},
		{Name: "release_tag", Column: "release_tag", Kind: query.KindString},
		{Name: "sha", Column: "sha", Kind: query.KindString},
		{Name: "branch", Column: "branch", Kind: query.KindString},
		{Name: "run_id", Column: "run_id", Kind: query.KindInt},
		{Name: "status", Column: "status", Kind: query.KindString},
		{Name: "created_unix", Column: "created_unix", Kind: query.KindTime},
	},
	SortFields:   []string{"id", "created_unix", "environment", "release_tag"},
	DefaultSort:  "created_unix",
	DefaultOrder: query.OrderDesc,
	PrimaryKey:   "id",
	SearchFields: []string{"release_tag", "environment"},
	// approval joined the list in slice 6, which is where the approvals resource is
	// declared. Whitelisting it before then would have published an expansion that always
	// returned nothing, which no test could tell from a broken one.
	Expands: []string{"release", "audit", "approval"},
	Paging:  query.PagingCursor,
}

// Deployment is the deployments resource's response shape: the model, plus whatever the
// request expanded (I9).
type Deployment struct {
	delivery.Deployment
	Release *Release               `json:"release,omitempty"`
	Audit   []*delivery.AuditEvent `json:"audit,omitempty"`
	// Approval is the hold the approval gate placed on this run, when there was one (F5).
	Approval *delivery.Approval `json:"approval,omitempty"`
}

func listDeploymentsEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "listDeployments", Method: http.MethodGet, Path: "/deployments",
			Summary: "List deployments across every repository the caller can see",
			Description: "Append-only: deploying a release to an environment twice leaves two rows, never one upserted row, " +
				"or the grid could not show that a version was deployed somewhere previously (E3). " +
				"Pages by cursor; the continuation token is in the " + NextCursorHeader + " header and in Link rel=next (I6). " +
				"Scoped by Gitea's own permission filtering on the Actions unit (E10, E12, I13).",
			Tag: "deployments", Query: &deploymentSpec, Response: "Deployment", ResponseIs: "array",
		},
		Handler: ListDeployments,
	}
}

// ListDeployments answers GET /deployments.
func ListDeployments(ctx *context.APIContext) {
	q, ok := parseCursorQuery(ctx, deploymentSpec)
	if !ok {
		return
	}
	repoIDs, ok := accessibleRepoIDs(ctx)
	if !ok {
		return
	}
	if len(repoIDs) == 0 {
		renderCursorPage(ctx, q, 0, nil, 0, []*Deployment{})
		return
	}

	cond := q.Cond().And(builder.In("repo_id", repoIDs)).And(q.CursorCond())
	rows, err := delivery.FindDeployments(ctx, cond, q.OrderBy(), q.Limit)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	out := make([]*Deployment, 0, len(rows))
	for _, row := range rows {
		out = append(out, &Deployment{Deployment: *row})
	}
	if err := expandDeployments(ctx, q.Expand, out); err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	sortValue, lastID := any(nil), int64(0)
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		lastID = last.ID
		sortValue = deploymentSortValue(q.Sort.Column, last)
	}
	renderCursorPage(ctx, q, len(rows), sortValue, lastID, out)
}

// deploymentSortValue reads the column the traversal is sorted on out of a row, so the
// cursor points at a position rather than an offset.
func deploymentSortValue(column string, d *delivery.Deployment) any {
	switch column {
	case "id":
		return d.ID
	case "environment":
		return d.Environment
	case "release_tag":
		return d.ReleaseTag
	}
	return int64(d.CreatedUnix)
}

// expandDeployments fills the whitelisted sub-resources, one level deep (I9).
func expandDeployments(ctx *context.APIContext, expand []string, rows []*Deployment) error {
	if len(rows) == 0 || len(expand) == 0 {
		return nil
	}
	for _, name := range expand {
		switch name {
		case "release":
			for _, row := range rows {
				release, err := repo_model.GetRelease(ctx, row.RepoID, row.ReleaseTag)
				if err != nil {
					// A deployment can outlive the release it names; that is a history
					// the log must keep, not an error to fail the page with.
					continue
				}
				row.Release = convertRelease(ctx, release)
			}
		case "audit":
			for _, row := range rows {
				cond := builder.Eq{"repo_id": row.RepoID, "run_id": row.RunID, "environment": row.Environment}
				events, err := delivery.FindAuditEvents(ctx, cond, "occurred_unix ASC, id ASC", query.MaxLimit)
				if err != nil {
					return err
				}
				row.Audit = events
			}
		case "approval":
			for _, row := range rows {
				cond := builder.Eq{"repo_id": row.RepoID, "run_id": row.RunID, "environment": row.Environment}
				holds, _, err := delivery.FindApprovals(ctx, cond, "id DESC", 1, 0)
				if err != nil {
					return err
				}
				if len(holds) > 0 {
					row.Approval = holds[0]
				}
			}
		}
	}
	return nil
}

// accessibleRepoIDs resolves the repositories the caller can see, through Gitea's existing
// permission filtering on the Actions unit — the same filter the grid uses (E10, E12, I13).
func accessibleRepoIDs(ctx *context.APIContext) ([]int64, bool) {
	ids, err := repo_model.SearchRepositoryIDsByCondition(ctx,
		repo_model.AccessibleRepositoryCondition(ctx.Doer, unit.TypeActions))
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}
	return ids, true
}
