// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"net/http"

	org_model "gitea.dev/models/organization"
	access_model "gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/templates"
	planningv1 "gitea.dev/routers/api/planning/v1"
	hub_web "gitea.dev/routers/web/hub"
	"gitea.dev/services/context"
)

const tplSettings templates.TplName = "planning/settings"

// settingsPageData builds ctx.PageData["planningSettings"], the shape both Settings and
// RepoSettings serve: apiBase and token are the same page-token pair every planning page
// mints, the rest tells the mounted client which scope it edits and whether it may.
func settingsPageData(ctx *context.Context, repoID, orgID int64, ownerName, repoFullName string, canWrite bool) map[string]any {
	hub_web.SetPageToken(ctx)
	token, _ := ctx.Data["PageToken"].(string)
	return map[string]any{
		"apiBase":      setting.AppSubURL + planningv1.BasePath,
		"token":        token,
		"repoId":       repoID,
		"orgId":        orgID,
		"ownerName":    ownerName,
		"repoFullName": repoFullName,
		"canWrite":     canWrite,
	}
}

// Settings renders /planning/settings/{owner}: an organization's or a user's own instance-wide
// scope. An organization's write control is its owner team; a user's is a site administrator,
// since a plain user names no scope of their own for a type, field or capacity row to live in.
func Settings(ctx *context.Context) {
	owner, err := user_model.GetUserByName(ctx, ctx.PathParam("owner"))
	if err != nil {
		ctx.NotFound(nil)
		return
	}
	if !org_model.HasOrgOrUserVisible(ctx, owner, ctx.Doer) {
		ctx.NotFound(nil)
		return
	}

	var doerID int64
	if ctx.Doer != nil {
		doerID = ctx.Doer.ID
	}

	var orgID int64
	canWrite := ctx.Doer != nil && ctx.Doer.IsAdmin
	if owner.IsOrganization() {
		org := org_model.OrgFromUser(owner)
		orgID = org.ID
		owned, err := org.IsOwnedBy(ctx, doerID)
		if err != nil {
			ctx.ServerError("IsOwnedBy", err)
			return
		}
		canWrite = owned
	}

	ctx.Data["Title"] = "Planning settings"
	ctx.Data["PageIsPlanning"] = true
	ctx.PageData["planningSettings"] = settingsPageData(ctx, 0, orgID, owner.Name, "", canWrite)
	ctx.HTML(http.StatusOK, tplSettings)
}

// RepoSettings renders /planning/settings/{owner}/{repo}: a repository's own scope, writable by
// its administrators, the same scope-admin rule the API itself enforces on every write.
func RepoSettings(ctx *context.Context) {
	repo, err := repo_model.GetRepositoryByOwnerAndName(ctx, ctx.PathParam("owner"), ctx.PathParam("repo"))
	if err != nil {
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
	ctx.Data["Title"] = "Planning settings"
	ctx.Data["PageIsPlanning"] = true
	ctx.PageData["planningSettings"] = settingsPageData(ctx, repo.ID, 0, repo.OwnerName, repo.FullName(), perm.IsAdmin())
	ctx.HTML(http.StatusOK, tplSettings)
}
