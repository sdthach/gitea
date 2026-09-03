// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package planning renders the fork's board and timeline pages. Each page is a client of a
// documented /api/planning/v1 endpoint: the handler serves the page shell and nothing else,
// and every figure on the page arrives over the public API.
package planning

import (
	"net/http"

	"gitea.dev/modules/setting"
	"gitea.dev/modules/templates"
	planningv1 "gitea.dev/routers/api/planning/v1"
	hub_web "gitea.dev/routers/web/hub"
	"gitea.dev/services/context"
	planning_service "gitea.dev/services/planning"
)

const (
	tplBoard    templates.TplName = "delivery/board"
	tplTimeline templates.TplName = "delivery/timeline"
)

// RouteRegistrar is the slice of *web.Router this package needs.
type RouteRegistrar interface {
	Get(pattern string, h ...any)
}

// RegisterRoutes mounts the planning pages. routers/web/hubroutes calls this from its own
// RegisterRoutes, behind hub's own settings gate.
func RegisterRoutes(m RouteRegistrar, reqSignIn any) {
	m.Get("/delivery/board", reqSignIn, hub_web.PlanningPagesEnabled, Board)
	m.Get("/delivery/timeline", reqSignIn, hub_web.PlanningPagesEnabled, Timeline)
}

// Board renders /delivery/board: Gitea's project columns vertically, with horizontal lanes
// Gitea does not model. Lane assignment, the empty-value lane and both writes are the
// server's, so the page and the CLI cannot disagree about which lane a card is in.
func Board(ctx *context.Context) {
	ctx.Data["Title"] = "Board"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + planningv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	ctx.Data["Groupings"] = planning_service.Groupings
	hub_web.SetPageToken(ctx)
	ctx.HTML(http.StatusOK, tplBoard)
}

// Timeline renders /delivery/timeline: one bar per issue with dependency arrows. It needs
// no Projects API, so it renders on a build whose board degrades. Every bar names the source
// of each endpoint, and an inferred end is drawn distinctly from a recorded one.
func Timeline(ctx *context.Context) {
	ctx.Data["Title"] = "Delivery timeline"
	ctx.Data["PageIsDelivery"] = true
	ctx.Data["DeliveryAPIBase"] = setting.AppSubURL + planningv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	hub_web.SetPageToken(ctx)
	ctx.HTML(http.StatusOK, tplTimeline)
}
