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
	tplEnvironment templates.TplName = "delivery/environment"
	tplGrid        templates.TplName = "delivery/grid"
)

// Environment renders the environment list, filtered to one name at
// /delivery/environments/{name}.
func Environment(ctx *context.Context) {
	name := ctx.PathParam("name")
	ctx.Data["Title"] = "Environments"
	if name != "" {
		ctx.Data["Title"] = "Environment: " + name
	}
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["EnvironmentName"] = name
	ctx.Data["EnvironmentID"] = ""
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deploymentsv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	hub_web.SetPageToken(ctx)
	ctx.HTML(http.StatusOK, tplEnvironment)
}

// EnvironmentEdit renders /delivery/environments/{id}/edit. Identity is the id, because a
// name is ambiguous across repositories and may itself be all digits.
func EnvironmentEdit(ctx *context.Context) {
	ctx.Data["Title"] = "Environment"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["EnvironmentName"] = ""
	ctx.Data["EnvironmentID"] = ctx.PathParam("id")
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deploymentsv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	hub_web.SetPageToken(ctx)
	ctx.HTML(http.StatusOK, tplEnvironment)
}

// Grid renders /delivery/grid: releases as rows, environments as columns. Cell states are
// projected server-side so the page and the CLI cannot disagree about what a cell means.
func Grid(ctx *context.Context) {
	ctx.Data["Title"] = "Delivery grid"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deploymentsv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	hub_web.SetPageToken(ctx)
	ctx.HTML(http.StatusOK, tplGrid)
}
