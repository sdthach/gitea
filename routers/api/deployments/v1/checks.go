// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	deployments_model "gitea.dev/models/deployments"
	"gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	deployments_service "gitea.dev/services/deployments"
)

var deploymentIDParam = []hubapi.Param{
	{Name: "id", In: "path", Type: "integer", Description: "Deployment id.", Required: true},
}

func deploymentChecksEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getDeploymentChecks", Method: http.MethodGet, Path: "/deployments/{id}/checks",
			Summary: "Re-evaluate a deployment's pre-deployment checks",
			Description: "Re-runs every check EvaluateChecks applies for a waiting or in-progress deployment: reviewers, " +
				"prior deployment, releases only, wait timer, deployment window, required status contexts and exclusive " +
				"lock, in that order. Scoped by Gitea's own permission filtering on the Actions unit.",
			Tag: "deployments", PathParams: deploymentIDParam,
			CLINames: []string{"deployment-checks"},
			Response: "Check", ResponseIs: "array",
		},
		Handler: GetDeploymentChecks,
	}
}

// GetDeploymentChecks answers GET /deployments/{id}/checks.
func GetDeploymentChecks(ctx *context.APIContext) {
	id, err := strconv.ParseInt(ctx.PathParam("id"), 10, 64)
	if err != nil || id <= 0 {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_id", "the id in the path is not a number",
			"Call GET "+BasePath+"/deployments/{id}/checks with an id from "+BasePath+"/deployments.")
		return
	}
	d, err := deployments_model.GetDeploymentByID(ctx, id)
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusNotFound, err)
		return
	}
	visible, err := deploymentIsVisible(ctx, d.RepoID)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if !visible { // one refusal for a hidden row and a missing one, so a 404 confirms nothing
		hubapi.APIError(ctx, http.StatusNotFound, "deployment_not_found",
			fmt.Sprintf("no deployment with id %d is visible to you", id),
			"List "+BasePath+"/deployments to see the deployments you can read.")
		return
	}

	env, err := deployments_model.GetEnvironment(ctx, d.RepoID, d.Environment)
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusNotFound, err)
		return
	}
	checks, err := deployments_service.EvaluateChecks(ctx, deployments_service.CheckContext{
		RepoID: d.RepoID, Env: env, ReleaseTag: d.ReleaseTag, SHA: d.SHA,
		RequestedUnix: int64(d.CreatedUnix), ExcludeDeploymentID: d.ID,
	}, time.Now().Unix())
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusOK, checks)
}

// deploymentIsVisible resolves repoID and reports whether the caller may read its Actions
// unit — the same rule ListDeployments applies to the rows it returns.
func deploymentIsVisible(ctx *context.APIContext, repoID int64) (bool, error) {
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			return false, nil
		}
		return false, err
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		return false, err
	}
	return perm.CanRead(unit.TypeActions), nil
}
