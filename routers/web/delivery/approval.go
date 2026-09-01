// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"net/http"

	"gitea.dev/modules/setting"
	"gitea.dev/modules/templates"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"
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
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deliveryv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	setPageToken(ctx)
	ctx.HTML(http.StatusOK, tplApprovals)
}
