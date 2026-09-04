// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"net/http"

	"gitea.dev/modules/setting"
	"gitea.dev/modules/templates"
	deploymentsv1 "gitea.dev/routers/api/deployments/v1"
	hub_web "gitea.dev/routers/web/hub"
	"gitea.dev/services/context"
)

const (
	tplEnvironment templates.TplName = "deployments/environment"
	tplMatrix      templates.TplName = "deployments/matrix"
)

// Environment renders the environment list, filtered to one name at
// /deployments/environments/{name}.
func Environment(ctx *context.Context) {
	name := ctx.PathParam("name")
	ctx.Data["Title"] = "Environments"
	if name != "" {
		ctx.Data["Title"] = "Environment: " + name
	}
	ctx.Data["PageIsDeployments"] = true
	ctx.Data["EnvironmentName"] = name
	ctx.Data["EnvironmentID"] = ""
	ctx.Data["APIBase"] = setting.AppSubURL + deploymentsv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	hub_web.SetPageToken(ctx)
	ctx.HTML(http.StatusOK, tplEnvironment)
}

// EnvironmentEdit renders /deployments/environments/{id}/edit. Identity is the id, because a
// name is ambiguous across repositories and may itself be all digits.
func EnvironmentEdit(ctx *context.Context) {
	ctx.Data["Title"] = "Environment"
	ctx.Data["PageIsDeployments"] = true
	ctx.Data["EnvironmentName"] = ""
	ctx.Data["EnvironmentID"] = ctx.PathParam("id")
	ctx.Data["APIBase"] = setting.AppSubURL + deploymentsv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	hub_web.SetPageToken(ctx)
	ctx.HTML(http.StatusOK, tplEnvironment)
}

// Matrix renders /deployments: releases as rows, environments as columns. Cell states are
// projected server-side so the page and the CLI cannot disagree about what a cell means.
func Matrix(ctx *context.Context) {
	ctx.Data["Title"] = "Deployments"
	ctx.Data["PageIsDeployments"] = true
	hub_web.SetPageToken(ctx)
	token, _ := ctx.Data["PageToken"].(string)
	ctx.PageData["deploymentsMatrix"] = map[string]any{
		"apiBase":   setting.AppSubURL + deploymentsv1.BasePath,
		"appSubUrl": setting.AppSubURL,
		"token":     token,
	}
	ctx.HTML(http.StatusOK, tplMatrix)
}
