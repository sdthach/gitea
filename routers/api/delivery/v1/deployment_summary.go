// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"
	"strings"

	delivery "gitea.dev/models/deployments"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/services/context"
	"gitea.dev/services/hub/query"

	"xorm.io/builder"
)

// DeploymentSummary is the denormalized projection the summary endpoint returns. Default
// columns are always present; optional ones appear when ?fields names them.
type DeploymentSummary struct {
	ID           int64  `json:"id"`
	RepoID       int64  `json:"repo_id"`
	RepoFullName string `json:"repo_full_name"`
	Environment  string `json:"environment"`
	ReleaseTag   string `json:"release_tag"`
	Status       string `json:"status"`
	Branch       string `json:"branch"`
	DeployedBy   string `json:"deployed_by"`
	DeployedAt   int64  `json:"deployed_at"`
	SHA          string `json:"sha,omitempty"`
	RunID        int64  `json:"run_id,omitempty"`
	RunURL       string `json:"run_url,omitempty"`
	ApprovedBy   string `json:"approved_by,omitempty"`
	ApprovedAt   int64  `json:"approved_at,omitempty"`
	Duration     int64  `json:"duration_seconds,omitempty"`
}

var summaryOptionalFields = map[string]bool{
	"sha": true, "run": true, "approved_by": true, "approved_at": true, "duration": true,
}

var deploymentSummarySpec = query.Spec{
	Resource: "deployment-summary",
	Fields: []query.Field{
		{Name: "id", Column: "id", Kind: query.KindInt},
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt},
		{Name: "environment", Column: "environment", Kind: query.KindString},
		{Name: "release_tag", Column: "release_tag", Kind: query.KindString},
		{Name: "status", Column: "status", Kind: query.KindString},
		{Name: "branch", Column: "branch", Kind: query.KindString},
		{Name: "created_unix", Column: "created_unix", Kind: query.KindTime},
	},
	SortFields:   []string{"id", "created_unix", "environment", "release_tag"},
	DefaultSort:  "created_unix",
	DefaultOrder: query.OrderDesc,
	PrimaryKey:   "id",
	SearchFields: []string{"release_tag", "environment"},
	Paging:       query.PagingCursor,
}

var summaryFieldsParam = []Param{
	{
		Name: "fields", In: "query", Type: "string",
		Description: "Comma-separated optional columns: sha, run, approved_by, approved_at, duration.",
	},
}

func getDeploymentSummaryEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "getDeploymentSummary", Method: http.MethodGet, Path: "/deployment-summary",
			Summary: "Deployment summary view",
			Description: "A denormalized projection of deployments with audit data joined. Default columns " +
				"(environment, release, status, branch, deployed_by, deployed_at) are always present; add " +
				"optional ones with ?fields=sha,run,approved_by,approved_at,duration.",
			Tag: "deployments", QueryParams: summaryFieldsParam,
			Query: &deploymentSummarySpec, Response: "DeploymentSummary", ResponseIs: "array",
		},
		Handler: GetDeploymentSummary,
	}
}

// GetDeploymentSummary answers GET /deployment-summary.
func GetDeploymentSummary(ctx *context.APIContext) {
	wantFields := parseSummaryFields(ctx.Req.URL.Query().Get("fields"))

	q, ok := parseCursorQuery(ctx, deploymentSummarySpec)
	if !ok {
		return
	}
	repoIDs, ok := accessibleRepoIDs(ctx)
	if !ok {
		return
	}
	if len(repoIDs) == 0 {
		renderCursorPage(ctx, q, 0, nil, 0, []*DeploymentSummary{})
		return
	}

	cond := q.Cond().And(builder.In("repo_id", repoIDs)).And(q.CursorCond())
	rows, err := delivery.FindDeployments(ctx, cond, q.OrderBy(), q.Limit)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	repoNames := make(map[int64]string)
	for _, row := range rows {
		if _, seen := repoNames[row.RepoID]; !seen {
			if repo, err := repo_model.GetRepositoryByID(ctx, row.RepoID); err == nil {
				repoNames[row.RepoID] = repo.FullName()
			}
		}
	}

	out := make([]*DeploymentSummary, 0, len(rows))
	for _, row := range rows {
		s := &DeploymentSummary{
			ID:           row.ID,
			RepoID:       row.RepoID,
			RepoFullName: repoNames[row.RepoID],
			Environment:  row.Environment,
			ReleaseTag:   row.ReleaseTag,
			Status:       row.Status,
			Branch:       row.Branch,
			DeployedAt:   int64(row.CreatedUnix),
		}
		if wantFields["sha"] {
			s.SHA = row.SHA
		}
		if wantFields["run"] {
			s.RunID = row.RunID
			s.RunURL = row.RunURL
		}
		out = append(out, s)
	}

	enrichSummaryFromAudit(ctx, rows, out, wantFields)

	sortValue, lastID := any(nil), int64(0)
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		lastID = last.ID
		sortValue = deploymentSortValue(q.Sort.Column, last)
	}
	renderCursorPage(ctx, q, len(rows), sortValue, lastID, out)
}

func enrichSummaryFromAudit(ctx *context.APIContext, deps []*delivery.Deployment, summaries []*DeploymentSummary, wantFields map[string]bool) {
	needApproved := wantFields["approved_by"] || wantFields["approved_at"]
	needDuration := wantFields["duration"]

	for i, dep := range deps {
		cond := builder.Eq{"repo_id": dep.RepoID, "run_id": dep.RunID, "environment": dep.Environment}
		events, err := delivery.FindAuditEvents(ctx, cond, "occurred_unix ASC, id ASC", query.MaxLimit)
		if err != nil {
			continue
		}

		var startedAt, stoppedAt int64
		for _, e := range events {
			switch e.Event {
			case delivery.AuditRequested:
				summaries[i].DeployedBy = e.ActorLogin
			case delivery.AuditApproved:
				if needApproved {
					summaries[i].ApprovedBy = e.ActorLogin
					summaries[i].ApprovedAt = int64(e.OccurredUnix)
				}
			case delivery.AuditStarted:
				if needDuration {
					startedAt = int64(e.OccurredUnix)
				}
			case delivery.AuditSucceeded, delivery.AuditFailed:
				if needDuration {
					stoppedAt = int64(e.OccurredUnix)
				}
			}
		}
		if needDuration && startedAt > 0 && stoppedAt > startedAt {
			summaries[i].Duration = stoppedAt - startedAt
		}
	}
}

func parseSummaryFields(raw string) map[string]bool {
	if raw == "" {
		return nil
	}
	result := make(map[string]bool)
	for f := range strings.SplitSeq(raw, ",") {
		f = strings.TrimSpace(f)
		if summaryOptionalFields[f] {
			result[f] = true
		}
	}
	return result
}
