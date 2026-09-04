// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"
	"strings"

	deployments_model "gitea.dev/models/deployments"
	repo_model "gitea.dev/models/repo"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	"gitea.dev/services/hub/query"

	"xorm.io/builder"
)

// InsightsDeployments is the denormalized projection the summary endpoint returns. Default
// columns are always present; optional ones appear when ?fields names them.
type InsightsDeployments struct {
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

var insightsDeploymentsOptionalFields = map[string]bool{
	"sha": true, "run": true, "approved_by": true, "approved_at": true, "duration": true,
}

var insightsDeploymentsSpec = query.Spec{
	Resource: "insights-deployments",
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

var insightsDeploymentsFieldsParam = []hubapi.Param{
	{
		Name: "fields", In: "query", Type: "string",
		Description: "Comma-separated optional columns: sha, run, approved_by, approved_at, duration.",
	},
}

func getInsightsDeploymentsEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getInsightsDeployments", Method: http.MethodGet, Path: "/insights/deployments",
			Summary: "Deployment summary view",
			Description: "A denormalized projection of deployments with audit data joined. Default columns " +
				"(environment, release, status, branch, deployed_by, deployed_at) are always present; add " +
				"optional ones with ?fields=sha,run,approved_by,approved_at,duration.",
			Tag: "insights", QueryParams: insightsDeploymentsFieldsParam,
			Query: &insightsDeploymentsSpec, Response: "InsightsDeployments", ResponseIs: "array",
		},
		Handler: GetInsightsDeployments,
	}
}

// GetInsightsDeployments answers GET /insights/deployments.
func GetInsightsDeployments(ctx *context.APIContext) {
	wantFields := parseInsightsDeploymentsFields(ctx.Req.URL.Query().Get("fields"))

	q, ok := hubapi.ParseCursorQuery(ctx, insightsDeploymentsSpec)
	if !ok {
		return
	}
	repoIDs, ok := accessibleRepoIDs(ctx)
	if !ok {
		return
	}
	if len(repoIDs) == 0 {
		hubapi.RenderCursorPage(ctx, q, 0, nil, 0, []*InsightsDeployments{})
		return
	}

	cond := q.Cond().And(builder.In("repo_id", repoIDs)).And(q.CursorCond())
	rows, err := deployments_model.FindDeployments(ctx, cond, q.OrderBy(), q.Limit)
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

	out := make([]*InsightsDeployments, 0, len(rows))
	for _, row := range rows {
		s := &InsightsDeployments{
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

	enrichInsightsDeploymentsFromAudit(ctx, rows, out, wantFields)

	sortValue, lastID := any(nil), int64(0)
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		lastID = last.ID
		sortValue = deploymentSortValue(q.Sort.Column, last)
	}
	hubapi.RenderCursorPage(ctx, q, len(rows), sortValue, lastID, out)
}

func enrichInsightsDeploymentsFromAudit(ctx *context.APIContext, deps []*deployments_model.Deployment, summaries []*InsightsDeployments, wantFields map[string]bool) {
	needApproved := wantFields["approved_by"] || wantFields["approved_at"]
	needDuration := wantFields["duration"]

	for i, dep := range deps {
		cond := builder.Eq{"repo_id": dep.RepoID, "run_id": dep.RunID, "environment": dep.Environment}
		events, err := deployments_model.FindAuditEvents(ctx, cond, "occurred_unix ASC, id ASC", query.MaxLimit)
		if err != nil {
			continue
		}

		var startedAt, stoppedAt int64
		for _, e := range events {
			switch e.Event {
			case deployments_model.AuditRequested:
				summaries[i].DeployedBy = e.ActorLogin
			case deployments_model.AuditApproved:
				if needApproved {
					summaries[i].ApprovedBy = e.ActorLogin
					summaries[i].ApprovedAt = int64(e.OccurredUnix)
				}
			case deployments_model.AuditStarted:
				if needDuration {
					startedAt = int64(e.OccurredUnix)
				}
			case deployments_model.AuditSucceeded, deployments_model.AuditFailed:
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

func parseInsightsDeploymentsFields(raw string) map[string]bool {
	if raw == "" {
		return nil
	}
	result := make(map[string]bool)
	for f := range strings.SplitSeq(raw, ",") {
		f = strings.TrimSpace(f)
		if insightsDeploymentsOptionalFields[f] {
			result[f] = true
		}
	}
	return result
}
