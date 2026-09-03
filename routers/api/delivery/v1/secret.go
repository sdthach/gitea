// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"

	"gitea.dev/models/db"
	delivery "gitea.dev/models/deployments"
	secret_model "gitea.dev/models/secret"
	"gitea.dev/services/context"
	"gitea.dev/services/hub/query"

	"xorm.io/builder"
)

// SecretName is what a secret endpoint returns: names, environment scope and metadata.
// A secret VALUE is never readable over any endpoint at any scope, which is why this
// type has no value field to forget to strip.
type SecretName struct {
	ID          int64  `json:"id"` // the row DELETE /secret-scopes/{id} takes; 0 when the name carries no scope
	Name        string `json:"name"`
	RepoID      int64  `json:"repo_id"`
	Environment string `json:"environment"`
	Scoped      bool   `json:"scoped"`
	// Exists reports whether Gitea still holds a secret of this name. A scope row that
	// outlives its secret must not read as a configured credential.
	Exists bool `json:"exists"`
}

var secretNameSpec = query.Spec{
	Resource: "secrets",
	Fields: []query.Field{
		{Name: "name", Column: "secret_name", Kind: query.KindString},
		{Name: "environment", Column: "environment", Kind: query.KindString},
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt},
	},
	SortFields:   []string{"name", "environment"},
	DefaultSort:  "name",
	DefaultOrder: query.OrderAsc,
	PrimaryKey:   "id",
	SearchFields: []string{"secret_name", "environment"},
	Paging:       query.PagingOffset,
}

func listRepoEnvironmentSecretsEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "listRepoEnvironmentSecrets", Method: http.MethodGet,
			Path:    "/repos/{owner}/{repo}/environments/{name}/secrets",
			Summary: "List the secret NAMES scoped to one environment",
			Description: "Returns names, environment scope and metadata only. A secret value is never readable " +
				"over any endpoint at any scope. Requires write on the repository's Actions unit.",
			Tag: "secrets",
			PathParams: append(append([]Param{}, ownerRepoParams...),
				Param{Name: "name", In: "path", Type: "string", Description: "Environment name.", Required: true}),
			Query: &secretNameSpec, Response: "SecretName", ResponseIs: "array",
		},
		Handler: ListRepoEnvironmentSecrets,
	}
}

// ListRepoEnvironmentSecrets answers GET /repos/{owner}/{repo}/environments/{name}/secrets.
func ListRepoEnvironmentSecrets(ctx *context.APIContext) {
	repo, ok := repoWithActions(ctx, true)
	if !ok {
		return
	}
	env, err := delivery.GetEnvironment(ctx, repo.ID, ctx.PathParam("name"))
	if err != nil {
		renderHubError(ctx, http.StatusNotFound, err)
		return
	}
	q, ok := parseQuery(ctx, secretNameSpec)
	if !ok {
		return
	}

	scope := builder.Eq{"repo_id": repo.ID, "environment": env.Name}
	rows, total, err := delivery.FindSecretScopes(ctx, q.Cond().And(scope), q.OrderBy(), q.Limit, q.Offset())
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	existing, err := db.Find[secret_model.Secret](ctx, secret_model.FindSecretsOptions{RepoID: repo.ID})
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	present := make(map[string]bool, len(existing))
	for _, s := range existing {
		present[delivery.NormalizeSecretName(s.Name)] = true
	}

	out := make([]*SecretName, 0, len(rows))
	for _, r := range rows {
		out = append(out, &SecretName{
			ID:          r.ID,
			Name:        r.SecretName,
			RepoID:      r.RepoID,
			Environment: r.Environment,
			Scoped:      true,
			Exists:      present[delivery.NormalizeSecretName(r.SecretName)],
		})
	}
	renderPage(ctx, q, total, out)
}
