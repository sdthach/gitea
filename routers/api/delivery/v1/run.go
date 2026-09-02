// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"fmt"
	"net/http"
	"strings"

	actions_model "gitea.dev/models/actions"
	"gitea.dev/models/delivery"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/services/context"
	delivery_service "gitea.dev/services/delivery"
	"gitea.dev/services/delivery/query"

	"xorm.io/builder"
)

// runSpec is the cross-repository run list's whitelist declaration — the list Gitea has no
// endpoint for, since its own Actions API lists runs one repository and one workflow at a
// time.
//
// It pages by page rather than by cursor: unlike the audit log this is not an append-only
// table being traversed, it is a filtered view whose caller wants a total to show beside the
// tiles, and X-Total-Count is what carries it.
var runSpec = query.Spec{
	Resource: "runs",
	Fields: []query.Field{
		{Name: "id", Column: "id", Kind: query.KindInt},
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt},
		{Name: "workflow_id", Column: "workflow_id", Kind: query.KindString},
		// status is filtered by the state NAME the overview publishes, never by Gitea's
		// internal integer. mapRunStatusFilters rewrites the value before the condition is
		// rendered; declaring it here is what makes an unknown state a 400 that names the
		// offender rather than a silently empty result.
		{Name: "status", Column: "status", Kind: query.KindString, Ops: []query.Op{query.OpEq, query.OpIn}},
		{Name: "event", Column: "event", Kind: query.KindString},
		{Name: "ref", Column: "ref", Kind: query.KindString},
		{Name: "title", Column: "title", Kind: query.KindString},
		{Name: "created_unix", Column: "created", Kind: query.KindTime},
		{Name: "started_unix", Column: "started", Kind: query.KindTime},
		{Name: "stopped_unix", Column: "stopped", Kind: query.KindTime},
	},
	SortFields:   []string{"id", "created_unix", "started_unix", "stopped_unix", "workflow_id"},
	DefaultSort:  "created_unix",
	DefaultOrder: query.OrderDesc,
	PrimaryKey:   "id",
	SearchFields: []string{"title", "workflow_id", "ref"},
	Paging:       query.PagingOffset,
}

// Run is the delivery view of an Actions run. It carries run_url so every row links out to
// Gitea's own run page: the overview duplicates no Gitea page.
type Run struct {
	ID              int64  `json:"id"`
	RepoID          int64  `json:"repo_id"`
	RepoFullName    string `json:"repo_full_name"`
	Index           int64  `json:"index"`
	Title           string `json:"title"`
	WorkflowID      string `json:"workflow_id"`
	Event           string `json:"event"`
	Ref             string `json:"ref"`
	CommitSHA       string `json:"commit_sha"`
	Status          string `json:"status"`
	RunURL          string `json:"run_url"`
	CreatedUnix     int64  `json:"created_unix"`
	StartedUnix     int64  `json:"started_unix"`
	StoppedUnix     int64  `json:"stopped_unix"`
	DurationSeconds int64  `json:"duration_seconds"`
}

func listRunsEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "listRuns", Method: http.MethodGet, Path: "/runs",
			Summary: "List Actions runs across every repository the caller can see",
			Description: "The cross-repository run list Gitea has no endpoint of its own for: its Actions API lists runs " +
				"one repository and one workflow at a time. " +
				"status filters on the state name the overview publishes — " + strings.Join(delivery_service.RunStateNames(), ", ") +
				" — not on Gitea's internal integer. " +
				"Every row carries run_url; the overview duplicates no Gitea page. " +
				"Scoped by Gitea's own permission filtering on the Actions unit.",
			Tag: "runs", Query: &runSpec, Response: "Run", ResponseIs: "array",
		},
		Handler: ListRuns,
	}
}

// ListRuns answers GET /runs.
func ListRuns(ctx *context.APIContext) {
	q, ok := parseQuery(ctx, runSpec)
	if !ok {
		return
	}
	if qErr := mapRunStatusFilters(q); qErr != nil {
		renderQueryError(ctx, qErr)
		return
	}
	repoIDs, ok := accessibleRepoIDs(ctx)
	if !ok {
		return
	}
	if len(repoIDs) == 0 {
		renderPage(ctx, q, 0, []*Run{})
		return
	}

	cond := q.Cond().And(builder.In("repo_id", repoIDs))
	runs, total, err := delivery.FindRuns(ctx, cond, q.OrderBy(), q.Limit, q.Offset())
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	out, err := convertRuns(ctx, runs)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	renderPage(ctx, q, total, out)
}

// mapRunStatusFilters rewrites a status filter's values from the state names the overview
// publishes onto the Actions status integers the column stores, so the one grammar renders
// the condition and this endpoint does not grow a second filter implementation.
//
// A state can cover more than one status — in_progress covers running and cancelling — so an
// eq on a multi-status state becomes an IN.
func mapRunStatusFilters(q *query.Query) *query.Error {
	for i := range q.Filters {
		f := &q.Filters[i]
		if f.Field.Name != "status" {
			continue
		}
		codes := make([]any, 0, len(f.Values))
		for _, raw := range f.Values {
			name, isString := raw.(string)
			if !isString {
				continue
			}
			mapped, known := delivery_service.RunStatusCodes(name)
			if !known {
				return &query.Error{
					Status:          http.StatusBadRequest,
					Code:            "unknown_run_status",
					Parameter:       "status",
					Message:         fmt.Sprintf("%q is not a run state this instance reports", name),
					Accepted:        delivery_service.RunStateNames(),
					SuggestedAction: "Filter on one of the accepted states: " + strings.Join(delivery_service.RunStateNames(), ", ") + ".",
				}
			}
			for _, code := range mapped {
				codes = append(codes, code)
			}
		}
		if len(codes) == 0 {
			continue
		}
		// One state can cover several statuses — in_progress covers running and
		// cancelling — so equality widens into a set. That is also why the field declares
		// no ne: the grammar renders no NOT IN, and a ne that silently matched only one of
		// the two statuses would be wrong rather than merely narrow.
		f.Values = codes
		f.Op = query.OpIn
	}
	return nil
}

// convertRuns renders the rows, resolving each run's repository so it can carry the link out
// to Gitea's own run page.
func convertRuns(ctx *context.APIContext, runs []*actions_model.ActionRun) ([]*Run, error) {
	if len(runs) == 0 {
		return []*Run{}, nil
	}
	ids := make([]int64, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.RepoID)
	}
	repos, err := repo_model.GetRepositoriesMapByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*Run, 0, len(runs))
	for _, r := range runs {
		row := &Run{
			ID:          r.ID,
			RepoID:      r.RepoID,
			Index:       r.Index,
			Title:       r.Title,
			WorkflowID:  r.WorkflowID,
			Event:       string(r.Event),
			Ref:         r.Ref,
			CommitSHA:   r.CommitSHA,
			Status:      string(delivery_service.RunStateOf(int(r.Status))),
			CreatedUnix: int64(r.Created),
			StartedUnix: int64(r.Started),
			StoppedUnix: int64(r.Stopped),
		}
		fact := delivery.RunFact{StartedUnix: row.StartedUnix, StoppedUnix: row.StoppedUnix}
		row.DurationSeconds = fact.DurationSeconds()
		if repo := repos[r.RepoID]; repo != nil {
			row.RepoFullName = repo.FullName()
			r.Repo = repo
			row.RunURL = r.HTMLURL(ctx)
		}
		out = append(out, row)
	}
	return out, nil
}
