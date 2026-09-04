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

const tplNew templates.TplName = "deployments/new"

// New renders /deployments/new. It is the confirm step: the page POSTs to /deployments
// with confirm=false first, then confirm=true when the human presses the button. The
// sequence rule is applied by the API, so the CLI is refused exactly where the page is.
func New(ctx *context.Context) {
	ctx.Data["Title"] = "New deployment"
	ctx.Data["PageIsDeployments"] = true
	hub_web.SetPageToken(ctx)
	token, _ := ctx.Data["PageToken"].(string)
	ctx.PageData["deploymentsNew"] = map[string]any{
		"apiBase":   setting.AppSubURL + deploymentsv1.BasePath,
		"appSubUrl": setting.AppSubURL,
		"token":     token,
	}
	ctx.HTML(http.StatusOK, tplNew)
}
