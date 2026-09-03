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
	ctx.Data["APIBase"] = setting.AppSubURL + deploymentsv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	hub_web.SetPageToken(ctx)
	ctx.HTML(http.StatusOK, tplNew)
}
