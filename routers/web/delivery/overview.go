// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"net/http"

	"gitea.dev/modules/setting"
	"gitea.dev/modules/templates"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	"gitea.dev/services/context"
	delivery_service "gitea.dev/services/deployments"
)

const tplOverview templates.TplName = "delivery/overview"

// CI renders /delivery/ci, the cross-repository CI/CD overview.
//
// Like every other fork page it is a client of documented endpoints and reads nothing the API
// does not serve: the handler passes the API base and the default window and
// nothing else. Every per-run, per-workflow and per-repository detail on it links out to
// Gitea's own page rather than to a reimplementation.
func CI(ctx *context.Context) {
	ctx.Data["Title"] = "CI/CD overview"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deliveryv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	ctx.Data["DefaultWindowDays"] = delivery_service.DefaultWindowDays
	ctx.HTML(http.StatusOK, tplOverview)
}
