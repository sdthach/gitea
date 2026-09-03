// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"

	delivery "gitea.dev/models/deployments"
	"gitea.dev/services/context"
	"gitea.dev/services/hub/query"

	"xorm.io/builder"
)

// auditSpec is the audit resource's whitelist declaration.
//
// The resource is READ-ONLY. It declares one GET operation and nothing else, and Routes
// mounts operations rather than handlers, so there is no POST, PATCH or DELETE to reach:
// the append-only guarantee is enforced at the route as well as at the table.
var auditSpec = query.Spec{
	Resource: "audit",
	Fields: []query.Field{
		{Name: "id", Column: "id", Kind: query.KindInt},
		{Name: "event", Column: "event", Kind: query.KindString},
		{Name: "occurred_unix", Column: "occurred_unix", Kind: query.KindTime},
		{Name: "actor_id", Column: "actor_id", Kind: query.KindInt},
		{Name: "actor_login", Column: "actor_login", Kind: query.KindString},
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt},
		{Name: "environment", Column: "environment", Kind: query.KindString},
		{Name: "release_tag", Column: "release_tag", Kind: query.KindString},
		{Name: "run_id", Column: "run_id", Kind: query.KindInt},
		{Name: "source", Column: "source", Kind: query.KindString},
	},
	SortFields:   []string{"id", "occurred_unix", "event", "environment", "release_tag"},
	DefaultSort:  "occurred_unix",
	DefaultOrder: query.OrderDesc,
	PrimaryKey:   "id",
	SearchFields: []string{"release_tag", "environment", "actor_login"},
	Paging:       query.PagingCursor,
}

func listAuditEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "listAudit", Method: http.MethodGet, Path: "/audit",
			Summary: "List delivery audit events across every repository the caller can see",
			Description: "One row per event, retained indefinitely; no purge or archive path exists. " +
				"actor_login is denormalized, so deleting the user from Gitea does not erase who deployed. " +
				"The resource is read-only: no POST, PATCH or DELETE is published. " +
				"Pages by cursor; the continuation token is in the " + NextCursorHeader + " header and in Link rel=next.",
			Tag: "audit", Query: &auditSpec, Response: "AuditEvent", ResponseIs: "array",
		},
		Handler: ListAudit,
	}
}

// ListAudit answers GET /audit.
func ListAudit(ctx *context.APIContext) {
	q, ok := parseCursorQuery(ctx, auditSpec)
	if !ok {
		return
	}
	repoIDs, ok := accessibleRepoIDs(ctx)
	if !ok {
		return
	}
	if len(repoIDs) == 0 {
		renderCursorPage(ctx, q, 0, nil, 0, []*delivery.AuditEvent{})
		return
	}

	cond := q.Cond().And(builder.In("repo_id", repoIDs)).And(q.CursorCond())
	rows, err := delivery.FindAuditEvents(ctx, cond, q.OrderBy(), q.Limit)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	sortValue, lastID := any(nil), int64(0)
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		lastID = last.ID
		sortValue = auditSortValue(q.Sort.Column, last)
	}
	renderCursorPage(ctx, q, len(rows), sortValue, lastID, rows)
}

// auditSortValue reads the column the traversal is sorted on out of a row.
func auditSortValue(column string, e *delivery.AuditEvent) any {
	switch column {
	case "id":
		return e.ID
	case "event":
		return e.Event
	case "environment":
		return e.Environment
	case "release_tag":
		return e.ReleaseTag
	}
	return int64(e.OccurredUnix)
}
