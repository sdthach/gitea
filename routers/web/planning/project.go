// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"net/http"
	"strconv"

	access_model "gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/templates"
	planningv1 "gitea.dev/routers/api/planning/v1"
	hub_web "gitea.dev/routers/web/hub"
	"gitea.dev/services/context"
)

const tplProject templates.TplName = "planning/project"

// Projects renders /planning/projects: the picker over every repository's Projects the doer
// can see.
func Projects(ctx *context.Context) {
	ctx.Data["Title"] = "Projects"
	ctx.Data["PageIsPlanning"] = true
	hub_web.SetPageToken(ctx)
	token, _ := ctx.Data["PageToken"].(string)
	ctx.PageData["planningProject"] = map[string]any{
		"apiBase":       setting.AppSubURL + planningv1.BasePath,
		"token":         token,
		"repoId":        int64(0),
		"repoFullName":  "",
		"projectId":     int64(0),
		"canWrite":      false,
		"canEditIssues": false,
	}
	ctx.HTML(http.StatusOK, tplProject)
}

// Project renders /planning/projects/{owner}/{repo}/{project_id}. The project itself is not
// resolved here — the page's own API call does that — so this handler only proves the doer
// may see the repository at all before serving the shell.
func Project(ctx *context.Context) {
	owner := ctx.PathParam("owner")
	repoName := ctx.PathParam("repo")
	repo, err := repo_model.GetRepositoryByOwnerAndName(ctx, owner, repoName)
	if err != nil {
		ctx.NotFound(nil)
		return
	}

	projectID, err := strconv.ParseInt(ctx.PathParam("project_id"), 10, 64)
	if err != nil || projectID <= 0 {
		ctx.NotFound(nil)
		return
	}

	var doerID int64
	if ctx.Doer != nil {
		doerID = ctx.Doer.ID
	}
	has, err := access_model.HasAnyUnitAccess(ctx, doerID, repo)
	if err != nil {
		ctx.ServerError("HasAnyUnitAccess", err)
		return
	}
	if !has {
		ctx.NotFound(nil)
		return
	}

	perm, err := access_model.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.ServerError("GetDoerRepoPermission", err)
		return
	}

	ctx.Data["Title"] = "Project"
	ctx.Data["PageIsPlanning"] = true
	hub_web.SetPageToken(ctx)
	token, _ := ctx.Data["PageToken"].(string)
	ctx.PageData["planningProject"] = map[string]any{
		"apiBase":       setting.AppSubURL + planningv1.BasePath,
		"token":         token,
		"repoId":        repo.ID,
		"repoFullName":  repo.FullName(),
		"projectId":     projectID,
		"canWrite":      perm.CanWrite(unit.TypeProjects),
		"canEditIssues": perm.CanWrite(unit.TypeIssues),
	}
	ctx.HTML(http.StatusOK, tplProject)
}
