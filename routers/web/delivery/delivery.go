// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package delivery renders the fork's pages. Each page is a client of a documented
// /api/delivery/v1 endpoint: the handler serves the page shell and nothing else, and every
// figure on the page arrives over the public API (E18, I14). A handler that reached past
// its own API into the models would be a defect.
package delivery

import (
	"net/http"

	"gitea.dev/modules/setting"
	"gitea.dev/modules/templates"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	"gitea.dev/services/context"
	delivery_service "gitea.dev/services/delivery"
)

const (
	tplEnvironment templates.TplName = "delivery/environment"
	tplGrid        templates.TplName = "delivery/grid"
)

// PagesEnabled is the settings gate, mirroring reqMilestonesDashboardPageEnabled so the
// whole feature can be switched off (F13).
func PagesEnabled(ctx *context.Context) {
	if !delivery_service.PagesEnabled() {
		ctx.HTTPError(http.StatusForbidden)
	}
}

// Environment renders /delivery/environments/{name}.
func Environment(ctx *context.Context) {
	name := ctx.PathParam("name")
	ctx.Data["Title"] = "Environment: " + name
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["EnvironmentName"] = name
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deliveryv1.BasePath
	ctx.HTML(http.StatusOK, tplEnvironment)
}

// Grid renders /delivery/grid: releases as rows, environments as columns (E7). Like every
// other page it is a client of a documented endpoint and reads nothing the API does not
// serve (E18, I14) — including the cell states, which are projected server-side so the page
// and the CLI cannot disagree about what a cell means.
func Grid(ctx *context.Context) {
	ctx.Data["Title"] = "Delivery grid"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + deliveryv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	ctx.HTML(http.StatusOK, tplGrid)
}

// RouteRegistrar is the slice of *web.Router this package needs. Taking an interface keeps
// the registration testable without standing up a router.
type RouteRegistrar interface {
	Get(pattern string, h ...any)
}

// RegisterRoutes mounts the fork's pages. It is the whole of the fork's web-route spoke:
// routers/web/web.go inserts one call to it beside /milestones (F2, F13). Each page sits
// behind reqSignIn and the settings gate.
func RegisterRoutes(m RouteRegistrar, reqSignIn any) {
	m.Get("/delivery/environments", reqSignIn, PagesEnabled, Environment)
	m.Get("/delivery/environments/{name}", reqSignIn, PagesEnabled, Environment)
	m.Get("/delivery/grid", reqSignIn, PagesEnabled, Grid)
	m.Get("/delivery/ci", reqSignIn, PagesEnabled, CI)                                   // slice 8
	m.Get("/delivery/promote", reqSignIn, PagesEnabled, Promote)                         // slice 5
	m.Get("/delivery/approvals", reqSignIn, PagesEnabled, Approvals)                     // slice 6
	m.Get("/delivery/environments/{name}/approvals", reqSignIn, PagesEnabled, Approvals) // slice 6
	m.Get("/delivery/board", reqSignIn, PagesEnabled, Board)                             // slice 7
	m.Get("/delivery/timeline", reqSignIn, PagesEnabled, Timeline)                       // slice 7
}
