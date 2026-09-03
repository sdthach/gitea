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

const tplApprovals templates.TplName = "delivery/approvals"

// Approvals renders /delivery/approvals, the pending-approval list an approver works from.
// A viewer without approval rights is offered no action because the endpoint says
// can_approve is false for them, which is the same check the approve endpoint enforces.
func Approvals(ctx *context.Context) {
	environment := ctx.PathParam("name")
	ctx.Data["Title"] = "Pending approvals"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["EnvironmentName"] = environment
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deploymentsv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	hub_web.SetPageToken(ctx)
	ctx.HTML(http.StatusOK, tplApprovals)
}
