// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"io"
	"net/http"
	"strconv"

	delivery "gitea.dev/models/deployments"
	"gitea.dev/modules/json"
	"gitea.dev/services/context"
)

const maxSecretScopeBody = 4 << 10

type secretScopeBody struct {
	RepoID      int64  `json:"repo_id"`
	SecretName  string `json:"secret_name"`
	Environment string `json:"environment"`
}

var secretScopeBodyParams = []Param{
	{Name: "repo_id", In: "body", Type: "integer", Required: true, Description: "Repository id."},
	{Name: "secret_name", In: "body", Type: "string", Required: true, Description: "Secret name as it appears in Gitea's Actions secrets."},
	{Name: "environment", In: "body", Type: "string", Required: true, Description: "Environment to scope the secret to."},
}

var secretScopeIDParam = []Param{
	{Name: "id", In: "path", Type: "integer", Description: "Secret scope id.", Required: true},
}

func createSecretScopeEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "createSecretScope", Method: http.MethodPost, Path: "/secret-scopes",
			Summary: "Bind a secret to an environment",
			Description: "Creates a scope row so the secret is only available to jobs declaring this environment. " +
				"A secret value is never accepted or returned. Authorized as repo admin.",
			Tag: "secrets", Body: secretScopeBodyParams,
			CLINames: []string{"secret-scope-create"},
			Response: "SecretScope", ResponseIs: "object",
		},
		Handler: CreateSecretScopeHandler,
	}
}

func deleteSecretScopeEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "deleteSecretScope", Method: http.MethodDelete, Path: "/secret-scopes/{id}",
			Summary:     "Unbind a secret from an environment",
			Description: "Removes the scope row, making the secret available to all environments again. Authorized as repo admin.",
			Tag:         "secrets", PathParams: secretScopeIDParam,
			CLINames: []string{"secret-scope-delete"},
			Response: "SecretScope", ResponseIs: "object",
		},
		Handler: DeleteSecretScopeHandler,
	}
}

// CreateSecretScopeHandler answers POST /secret-scopes.
func CreateSecretScopeHandler(ctx *context.APIContext) {
	body, ok := readSecretScopeBody(ctx)
	if !ok {
		return
	}
	if !requireEnvironmentAdmin(ctx, body.RepoID) {
		return
	}
	scope := &delivery.SecretScope{
		RepoID:      body.RepoID,
		SecretName:  body.SecretName,
		Environment: body.Environment,
	}
	if err := delivery.BindSecretScope(ctx, scope); err != nil {
		renderHubError(ctx, http.StatusBadRequest, err)
		return
	}
	ctx.JSON(http.StatusCreated, scope)
}

// DeleteSecretScopeHandler answers DELETE /secret-scopes/{id}.
func DeleteSecretScopeHandler(ctx *context.APIContext) {
	id, err := strconv.ParseInt(ctx.PathParam("id"), 10, 64)
	if err != nil || id <= 0 {
		apiError(ctx, http.StatusBadRequest, "bad_id", "the id in the path is not a number",
			"Call DELETE "+BasePath+"/secret-scopes/{id} with an id from the secret scopes listing.")
		return
	}
	existing, err := delivery.GetSecretScopeByID(ctx, id)
	if err != nil {
		renderHubError(ctx, http.StatusNotFound, err)
		return
	}
	if !requireEnvironmentAdmin(ctx, existing.RepoID) {
		return
	}
	if err := delivery.UnbindSecretScope(ctx, id); err != nil {
		renderHubError(ctx, http.StatusNotFound, err)
		return
	}
	ctx.JSON(http.StatusNoContent, nil)
}

func readSecretScopeBody(ctx *context.APIContext) (*secretScopeBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(ctx.Req.Body, maxSecretScopeBody+1))
	if err != nil {
		apiError(ctx, http.StatusBadRequest, "unreadable_body",
			"the request body could not be read", "Retry the request.")
		return nil, false
	}
	if len(raw) > maxSecretScopeBody {
		apiError(ctx, http.StatusBadRequest, "body_too_large",
			"the request body is larger than 4KiB",
			"A scope binding names a secret and an environment; it is small.")
		return nil, false
	}
	body := new(secretScopeBody)
	if err := json.Unmarshal(raw, body); err != nil {
		apiError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"repo_id": 1, "secret_name": "DEPLOY_KEY", "environment": "prod"}.`)
		return nil, false
	}
	return body, true
}
