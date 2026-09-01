// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"

	"gitea.dev/models/delivery"
	"gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/services/context"
	"gitea.dev/services/delivery/query"

	"xorm.io/builder"
)

// environmentSpec is the environment resource's whitelist declaration. It restates no part
// of the grammar (I2).
var environmentSpec = query.Spec{
	Resource: "environments",
	Fields: []query.Field{
		{Name: "id", Column: "id", Kind: query.KindInt},
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt},
		{Name: "name", Column: "name", Kind: query.KindString},
		{Name: "sort_order", Column: "sort_order", Kind: query.KindInt},
		{Name: "approval_policy", Column: "approval_policy", Kind: query.KindString},
		{Name: "required_approvals", Column: "required_approvals", Kind: query.KindInt},
		{Name: "created_unix", Column: "created_unix", Kind: query.KindTime},
		{Name: "updated_unix", Column: "updated_unix", Kind: query.KindTime},
	},
	SortFields:   []string{"id", "name", "sort_order", "created_unix", "updated_unix"},
	DefaultSort:  "sort_order",
	DefaultOrder: query.OrderAsc,
	PrimaryKey:   "id",
	SearchFields: []string{"name", "approval_policy"},
	Paging:       query.PagingOffset,
}

var ownerRepoParams = []Param{
	{Name: "owner", In: "path", Type: "string", Description: "Repository owner.", Required: true},
	{Name: "repo", In: "path", Type: "string", Description: "Repository name.", Required: true},
}

func listEnvironmentsEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "listEnvironments", Method: http.MethodGet, Path: "/environments",
			Summary: "List environments across every repository the caller can see",
			Description: "Scoped by Gitea's own permission filtering, plus the instance-wide default set (repo_id 0). " +
				"The /delivery/environments/{name} page is a client of this endpoint (E18, I14).",
			Tag: "environments", Query: &environmentSpec, Response: "Environment", ResponseIs: "array",
		},
		Handler: ListEnvironments,
	}
}

func listRepoEnvironmentsEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "listRepoEnvironments", Method: http.MethodGet, Path: "/repos/{owner}/{repo}/environments",
			Summary:     "List one repository's environments",
			Description: "Falls back to the instance-wide default set when the repository has declared none of its own.",
			Tag:         "environments", PathParams: ownerRepoParams,
			Query: &environmentSpec, Response: "Environment", ResponseIs: "array",
		},
		Handler: ListRepoEnvironments,
	}
}

func getRepoEnvironmentEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "getRepoEnvironment", Method: http.MethodGet, Path: "/repos/{owner}/{repo}/environments/{name}",
			Summary:     "Read one environment",
			Description: "Reads the repository's own row, falling back to the instance-wide default of that name.",
			Tag:         "environments",
			PathParams: append(append([]Param{}, ownerRepoParams...),
				Param{Name: "name", In: "path", Type: "string", Description: "Environment name.", Required: true}),
			Response: "Environment", ResponseIs: "object",
		},
		Handler: GetRepoEnvironment,
	}
}

// ListEnvironments answers GET /environments.
func ListEnvironments(ctx *context.APIContext) {
	q, ok := parseQuery(ctx, environmentSpec)
	if !ok {
		return
	}
	repoIDs, err := repo_model.SearchRepositoryIDsByCondition(ctx,
		repo_model.AccessibleRepositoryCondition(ctx.Doer, unit.TypeActions))
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	// The instance-wide default set is visible to every signed-in user; it holds no
	// repository data, only the configured environment names and their policies.
	visible := builder.Eq{"repo_id": delivery.DefaultsRepoID}.Or(builder.In("repo_id", repoIDs))
	envs, total, err := delivery.FindEnvironments(ctx, q.Cond().And(visible), q.OrderBy(), q.Limit, q.Offset())
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	renderPage(ctx, q, total, envs)
}

// ListRepoEnvironments answers GET /repos/{owner}/{repo}/environments.
func ListRepoEnvironments(ctx *context.APIContext) {
	repo, ok := repoWithActions(ctx, false)
	if !ok {
		return
	}
	q, ok := parseQuery(ctx, environmentSpec)
	if !ok {
		return
	}
	scope := builder.Eq{"repo_id": repo.ID}
	envs, total, err := delivery.FindEnvironments(ctx, q.Cond().And(scope), q.OrderBy(), q.Limit, q.Offset())
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if total == 0 && len(q.Filters) == 0 && q.Search == "" {
		defaults := builder.Eq{"repo_id": delivery.DefaultsRepoID}
		envs, total, err = delivery.FindEnvironments(ctx, defaults, q.OrderBy(), q.Limit, q.Offset())
		if err != nil {
			ctx.APIErrorInternal(err)
			return
		}
	}
	renderPage(ctx, q, total, envs)
}

// GetRepoEnvironment answers GET /repos/{owner}/{repo}/environments/{name}.
func GetRepoEnvironment(ctx *context.APIContext) {
	repo, ok := repoWithActions(ctx, false)
	if !ok {
		return
	}
	env, err := delivery.GetEnvironment(ctx, repo.ID, ctx.PathParam("name"))
	if err != nil {
		renderHubError(ctx, http.StatusNotFound, err)
		return
	}
	ctx.JSON(http.StatusOK, env)
}

// repoWithActions resolves {owner}/{repo} and authorizes through Gitea's own permission
// check on the Actions unit — the same check the rest of Gitea makes, never a second model
// of permissions (E10, I13).
func repoWithActions(ctx *context.APIContext, needWrite bool) (*repo_model.Repository, bool) {
	owner, name := ctx.PathParam("owner"), ctx.PathParam("repo")
	repo, err := repo_model.GetRepositoryByOwnerAndName(ctx, owner, name)
	if err != nil {
		apiError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository "+owner+"/"+name+" is visible to you",
			"Check the owner and repository name, and that your token's account can see the repository.")
		return nil, false
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}
	allowed := perm.CanRead(unit.TypeActions)
	if needWrite {
		allowed = perm.CanWrite(unit.TypeActions)
	}
	if !allowed {
		verb := "read"
		if needWrite {
			verb = "write"
		}
		apiError(ctx, http.StatusForbidden, "forbidden",
			"your account has no "+verb+" access to the Actions unit of "+repo.FullName(),
			"Ask a repository administrator for "+verb+" permission on Actions, or enable the Actions unit on the repository.")
		return nil, false
	}
	return repo, true
}
