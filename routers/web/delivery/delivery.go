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
}
