// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package delivery renders the fork's pages. Each page is a client of a documented
// /api/delivery/v1 endpoint: the handler serves the page shell and nothing else, and every
// figure on the page arrives over the public API.
package delivery

import (
	"net/http"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/modules/log"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/templates"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	"gitea.dev/services/context"
	delivery_service "gitea.dev/services/hub"
)

const (
	tplEnvironment templates.TplName = "delivery/environment"
	tplGrid        templates.TplName = "delivery/grid"
)

// PagesEnabled is the settings gate, mirroring reqMilestonesDashboardPageEnabled so the
// whole feature can be switched off.
func PagesEnabled(ctx *context.Context) {
	if !delivery_service.PagesEnabled() {
		ctx.HTTPError(http.StatusForbidden)
	}
}

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
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deliveryv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	setPageToken(ctx)
	ctx.HTML(http.StatusOK, tplEnvironment)
}

// EnvironmentEdit renders /delivery/environments/{id}/edit. Identity is the id, because a
// name is ambiguous across repositories and may itself be all digits.
func EnvironmentEdit(ctx *context.Context) {
	ctx.Data["Title"] = "Environment"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["EnvironmentName"] = ""
	ctx.Data["EnvironmentID"] = ctx.PathParam("id")
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deliveryv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	setPageToken(ctx)
	ctx.HTML(http.StatusOK, tplEnvironment)
}

// Grid renders /delivery/grid: releases as rows, environments as columns. Cell states are
// projected server-side so the page and the CLI cannot disagree about what a cell means.
func Grid(ctx *context.Context) {
	ctx.Data["Title"] = "Delivery grid"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deliveryv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	setPageToken(ctx)
	ctx.HTML(http.StatusOK, tplGrid)
}

// setPageToken mints a short-lived API token so the page's JS can call the delivery API
// without the user pasting a token manually. Reads already use the browser session; this
// covers writes only. The token is scoped to repository and issue writes.
func setPageToken(ctx *context.Context) {
	if ctx.Doer == nil {
		return
	}
	t := &auth_model.AccessToken{
		UID:   ctx.Doer.ID,
		Name:  "_delivery_page",
		Scope: auth_model.AccessTokenScopeWriteRepository + "," + auth_model.AccessTokenScopeWriteIssue,
	}
	if err := auth_model.NewAccessToken(ctx, t); err != nil {
		log.Error("delivery: mint page token for %s: %v", ctx.Doer.Name, err)
		return
	}
	ctx.Data["DeliveryPageToken"] = t.Token
}

// RouteRegistrar is the slice of *web.Router this package needs. Taking an interface keeps
// the registration testable without standing up a router.
type RouteRegistrar interface {
	Get(pattern string, h ...any)
}

// RegisterRoutes mounts the fork's pages. routers/web/web.go inserts one call to it beside
// /milestones. Each page sits behind reqSignIn and the settings gate.
func RegisterRoutes(m RouteRegistrar, reqSignIn any) {
	m.Get("/delivery/environments", reqSignIn, PagesEnabled, Environment)
	m.Get("/delivery/environments/{name}", reqSignIn, PagesEnabled, Environment)
	m.Get("/delivery/environments/{id}/edit", reqSignIn, PagesEnabled, EnvironmentEdit)
	m.Get("/delivery/grid", reqSignIn, PagesEnabled, Grid)
	m.Get("/delivery/ci", reqSignIn, PagesEnabled, CI)
	m.Get("/delivery/promote", reqSignIn, PagesEnabled, Promote)
	m.Get("/delivery/approvals", reqSignIn, PagesEnabled, Approvals)
	m.Get("/delivery/environments/{name}/approvals", reqSignIn, PagesEnabled, Approvals)
	m.Get("/delivery/board", reqSignIn, PagesEnabled, Board)
	m.Get("/delivery/timeline", reqSignIn, PagesEnabled, Timeline)
}
