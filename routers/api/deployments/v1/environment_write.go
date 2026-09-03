// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"fmt"
	"io"
	"net/http"
	"strconv"

	deployments_model "gitea.dev/models/deployments"
	"gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/modules/json"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
)

const maxEnvironmentBody = 16 << 10

type environmentBody struct {
	RepoID                 int64   `json:"repo_id"`
	Name                   string  `json:"name"`
	SortOrder              int64   `json:"sort_order"`
	ApprovalPolicy         string  `json:"approval_policy"`
	RequiredApprovals      int64   `json:"required_approvals"`
	Predecessor            string  `json:"predecessor"`
	RequirePredecessor     bool    `json:"require_predecessor"`
	RequireFullRelease     bool    `json:"require_full_release"`
	BlockAdminOverride     bool    `json:"block_admin_override"`
	EnableBypassAllowlist  bool    `json:"enable_bypass_allowlist"`
	BypassAllowlistUserIDs []int64 `json:"bypass_allowlist_user_ids"`
	BypassAllowlistTeamIDs []int64 `json:"bypass_allowlist_team_ids"`
}

var environmentBodyParams = []hubapi.Param{
	{Name: "repo_id", In: "body", Type: "integer", Description: "Repository id; 0 is the instance-wide default set."},
	{Name: "name", In: "body", Type: "string", Required: true, Description: "Environment name."},
	{Name: "sort_order", In: "body", Type: "integer", Description: "Render order."},
	{Name: "approval_policy", In: "body", Type: "string", Description: "Approval policy. Defaults to none.", Enum: deployments_model.ApprovalPolicies},
	{Name: "required_approvals", In: "body", Type: "integer", Description: "Approvals needed. Defaults to 1."},
	{Name: "predecessor", In: "body", Type: "string", Description: "Environment a release must pass through first."},
	{Name: "require_predecessor", In: "body", Type: "boolean", Description: "Gate when the predecessor hasn't held the release."},
	{Name: "require_full_release", In: "body", Type: "boolean", Description: "Refuse prereleases; this environment takes finished releases only."},
	{Name: "block_admin_override", In: "body", Type: "boolean", Description: "Block repo admin from bypassing the gate."},
	{Name: "enable_bypass_allowlist", In: "body", Type: "boolean", Description: "Enable bypass allowlist."},
	{Name: "bypass_allowlist_user_ids", In: "body", Type: "array", Description: "User IDs allowed to bypass."},
	{Name: "bypass_allowlist_team_ids", In: "body", Type: "array", Description: "Team IDs allowed to bypass."},
}

var environmentIDParam = []hubapi.Param{
	{Name: "id", In: "path", Type: "integer", Description: "Environment id.", Required: true},
}

func createEnvironmentEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "createEnvironment", Method: http.MethodPost, Path: "/environments",
			Summary: "Create an environment",
			Description: "Site admin for instance-wide defaults (repo_id 0), repo admin for " +
				"repository environments. Validates name, approval policy and predecessor.",
			Tag: "environments", Body: environmentBodyParams,
			CLINames: []string{"environment-create"},
			Response: "Environment", ResponseIs: "object",
		},
		Handler: CreateEnvironmentHandler,
	}
}

func updateEnvironmentEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "updateEnvironment", Method: http.MethodPut, Path: "/environments/{id}",
			Summary: "Update an environment",
			Description: "Replaces the environment's configuration. Site admin for instance-wide " +
				"defaults, repo admin for repository environments.",
			Tag: "environments", PathParams: environmentIDParam, Body: environmentBodyParams,
			CLINames: []string{"environment-update"},
			Response: "Environment", ResponseIs: "object",
		},
		Handler: UpdateEnvironmentHandler,
	}
}

func deleteEnvironmentEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "deleteEnvironment", Method: http.MethodDelete, Path: "/environments/{id}",
			Summary:     "Delete an environment",
			Description: "Same authorization as create and update.",
			Tag:         "environments", PathParams: environmentIDParam,
			CLINames: []string{"environment-delete"},
			Response: "Environment", ResponseIs: "object",
		},
		Handler: DeleteEnvironmentHandler,
	}
}

// canWriteEnvironment is the one write rule; the gate and each row's can_write both read it.
func canWriteEnvironment(ctx *context.APIContext, repoID int64) (bool, error) {
	if repoID == deployments_model.DefaultsRepoID {
		return ctx.Doer.IsAdmin, nil
	}
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		return false, err
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		return false, err
	}
	return perm.IsAdmin(), nil
}

// requireEnvironmentAdmin renders the refusal canWriteEnvironment implies.
func requireEnvironmentAdmin(ctx *context.APIContext, repoID int64) bool {
	allowed, err := canWriteEnvironment(ctx, repoID)
	switch {
	case repo_model.IsErrRepoNotExist(err):
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			fmt.Sprintf("no repository with id %d is visible to you", repoID),
			"Check the repo_id and that your token's account can see the repository.")
		return false
	case err != nil:
		ctx.APIErrorInternal(err)
		return false
	case allowed:
		return true
	case repoID == deployments_model.DefaultsRepoID:
		hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
			"only site administrators may manage instance-wide default environments",
			"Ask a Gitea administrator, or create a repository-specific environment instead.")
		return false
	}
	hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
		"only repository administrators may manage environments of "+environmentRepoName(ctx, repoID),
		"Ask a repository administrator for admin access.")
	return false
}

func environmentRepoName(ctx *context.APIContext, repoID int64) string {
	if repo, err := repo_model.GetRepositoryByID(ctx, repoID); err == nil {
		return repo.FullName()
	}
	return fmt.Sprintf("repository %d", repoID)
}

// CreateEnvironmentHandler answers POST /environments.
func CreateEnvironmentHandler(ctx *context.APIContext) {
	body, ok := readEnvironmentBody(ctx)
	if !ok {
		return
	}
	if !requireEnvironmentAdmin(ctx, body.RepoID) {
		return
	}
	env := bodyToEnvironment(body)
	if err := deployments_model.CreateEnvironment(ctx, env); err != nil {
		hubapi.RenderHubError(ctx, http.StatusBadRequest, err)
		return
	}
	renderEnvironment(ctx, http.StatusCreated, env)
}

// UpdateEnvironmentHandler answers PUT /environments/{id}.
func UpdateEnvironmentHandler(ctx *context.APIContext) {
	id, err := strconv.ParseInt(ctx.PathParam("id"), 10, 64)
	if err != nil || id <= 0 {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_id", "the id in the path is not a number",
			"Call PUT "+BasePath+"/environments/{id} with an id from "+BasePath+"/environments.")
		return
	}
	existing, err := deployments_model.GetEnvironmentByID(ctx, id)
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusNotFound, err)
		return
	}
	if !requireEnvironmentAdmin(ctx, existing.RepoID) {
		return
	}
	body, ok := readEnvironmentBody(ctx)
	if !ok {
		return
	}
	env := bodyToEnvironment(body)
	env.ID = id
	env.RepoID = existing.RepoID
	if err := deployments_model.UpdateEnvironment(ctx, env); err != nil {
		hubapi.RenderHubError(ctx, http.StatusBadRequest, err)
		return
	}
	renderEnvironment(ctx, http.StatusOK, env)
}

// DeleteEnvironmentHandler answers DELETE /environments/{id}.
func DeleteEnvironmentHandler(ctx *context.APIContext) {
	id, err := strconv.ParseInt(ctx.PathParam("id"), 10, 64)
	if err != nil || id <= 0 {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_id", "the id in the path is not a number",
			"Call DELETE "+BasePath+"/environments/{id} with an id from "+BasePath+"/environments.")
		return
	}
	existing, err := deployments_model.GetEnvironmentByID(ctx, id)
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusNotFound, err)
		return
	}
	if !requireEnvironmentAdmin(ctx, existing.RepoID) {
		return
	}
	if err := deployments_model.DeleteEnvironment(ctx, id); err != nil {
		hubapi.RenderHubError(ctx, http.StatusNotFound, err)
		return
	}
	ctx.JSON(http.StatusNoContent, nil)
}

func bodyToEnvironment(body *environmentBody) *deployments_model.Environment {
	return &deployments_model.Environment{
		RepoID:                 body.RepoID,
		Name:                   body.Name,
		SortOrder:              body.SortOrder,
		ApprovalPolicy:         body.ApprovalPolicy,
		RequiredApprovals:      body.RequiredApprovals,
		Predecessor:            body.Predecessor,
		RequirePredecessor:     body.RequirePredecessor,
		RequireFullRelease:     body.RequireFullRelease,
		BlockAdminOverride:     body.BlockAdminOverride,
		EnableBypassAllowlist:  body.EnableBypassAllowlist,
		BypassAllowlistUserIDs: body.BypassAllowlistUserIDs,
		BypassAllowlistTeamIDs: body.BypassAllowlistTeamIDs,
	}
}

func readEnvironmentBody(ctx *context.APIContext) (*environmentBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(ctx.Req.Body, maxEnvironmentBody+1))
	if err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "unreadable_body",
			"the request body could not be read", "Retry the request.")
		return nil, false
	}
	if len(raw) > maxEnvironmentBody {
		hubapi.APIError(ctx, http.StatusBadRequest, "body_too_large",
			"the request body is larger than 16KiB",
			"An environment definition is small; check whether the request is sending the right content.")
		return nil, false
	}
	body := new(environmentBody)
	if err := json.Unmarshal(raw, body); err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"name": "staging", "approval_policy": "none"}.`)
		return nil, false
	}
	return body, true
}
