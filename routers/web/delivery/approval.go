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

// Approvals renders /delivery/approvals, the pending-approval list an approver works from
// so they need not hunt the grid (E16).
//
// Like every other fork page it is a client of a documented endpoint and reads nothing the
// API does not serve (E18, I14): requester, release tag, age, run link and whether the
// viewer may act all arrive from GET /api/delivery/v1/approvals. A viewer without approval
// rights is offered no action because the endpoint says can_approve is false for them, which
// is the same check the approve endpoint enforces (SC 21).
func Approvals(ctx *context.Context) {
	environment := ctx.PathParam("name")
	ctx.Data["Title"] = "Pending approvals"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["EnvironmentName"] = environment
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deliveryv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	ctx.HTML(http.StatusOK, tplApprovals)
}
