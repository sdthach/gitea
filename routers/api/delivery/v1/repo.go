// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"

	"gitea.dev/models/db"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/services/context"
	"gitea.dev/services/delivery/query"
)

// Repository is the delivery view of a repository: the identity the grid, the environment
// page and the CLI address a repository by, and nothing more.
type Repository struct {
	ID       int64  `json:"id"`
	Owner    string `json:"owner"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
}

var repoSpec = query.Spec{
	Resource: "repos",
	Fields: []query.Field{
		{Name: "id", Column: "id", Kind: query.KindInt},
		{Name: "owner_name", Column: "owner_name", Kind: query.KindString},
		{Name: "name", Column: "lower_name", Kind: query.KindString},
		{Name: "is_archived", Column: "is_archived", Kind: query.KindBool},
	},
	SortFields:   []string{"id", "owner_name", "name"},
	DefaultSort:  "id",
	DefaultOrder: query.OrderAsc,
	PrimaryKey:   "id",
	SearchFields: []string{"owner_name", "lower_name"},
	Paging:       query.PagingOffset,
}

func listReposEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "listRepos", Method: http.MethodGet, Path: "/repos",
			Summary: "List the repositories the caller can see delivery data for",
			Description: "Scoped by Gitea's own permission filtering on the Actions unit, the same filter the " +
				"cross-repository grid uses.",
			Tag: "repos", Query: &repoSpec, Response: "Repository", ResponseIs: "array",
		},
		Handler: ListRepos,
	}
}

// ListRepos answers GET /repos.
func ListRepos(ctx *context.APIContext) {
	q, ok := parseQuery(ctx, repoSpec)
	if !ok {
		return
	}
	cond := q.Cond().And(repo_model.AccessibleRepositoryCondition(ctx.Doer, unit.TypeActions))
	opts := repo_model.SearchRepoOptions{OrderBy: db.SearchOrderBy(q.OrderBy())}
	opts.Page, opts.PageSize = q.Page, q.Limit
	repos, total, err := repo_model.SearchRepositoryByCondition(ctx, opts, cond, false)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	out := make([]*Repository, 0, len(repos))
	for _, r := range repos {
		out = append(out, &Repository{ID: r.ID, Owner: r.OwnerName, Name: r.Name, FullName: r.FullName()})
	}
	renderPage(ctx, q, total, out)
}
