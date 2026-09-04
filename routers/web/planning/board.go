// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package planning renders the fork's board and roadmap pages. Each page is a client of a
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
	tplBoard   templates.TplName = "planning/board"
	tplRoadmap templates.TplName = "planning/roadmap"
)

// RouteRegistrar is the slice of *web.Router this package needs.
type RouteRegistrar interface {
	Get(pattern string, h ...any)
	Post(pattern string, h ...any)
}

// RegisterRoutes mounts the planning pages. routers/web/hubroutes calls this from its own
// RegisterRoutes, behind hub's own settings gate.
func RegisterRoutes(m RouteRegistrar, reqSignIn any) {
	m.Get("/planning/projects", reqSignIn, hub_web.PlanningPagesEnabled, Projects)
	m.Get("/planning/projects/{owner}/{repo}/{project_id}", reqSignIn, hub_web.PlanningPagesEnabled, Project)
	m.Get("/planning/board", reqSignIn, hub_web.PlanningPagesEnabled, Board)
	m.Get("/planning/roadmap", reqSignIn, hub_web.PlanningPagesEnabled, Roadmap)
	m.Post("/planning/issues/{id}/schedule", reqSignIn, hub_web.PlanningPagesEnabled, ScheduleIssue)
	m.Post("/planning/issues/{id}/type", reqSignIn, hub_web.PlanningPagesEnabled, TypeIssue)
	m.Post("/planning/issues/{id}/parent", reqSignIn, hub_web.PlanningPagesEnabled, ParentIssue)
	m.Post("/planning/issues/{id}/fields", reqSignIn, hub_web.PlanningPagesEnabled, FieldsIssue)
	m.Post("/planning/issues/{id}/estimate", reqSignIn, hub_web.PlanningPagesEnabled, EstimateIssue)
	m.Post("/planning/milestones/{id}/schedule", reqSignIn, hub_web.PlanningPagesEnabled, ScheduleMilestone)
}

// Board renders /planning/board: Gitea's project columns vertically, with horizontal groups
// Gitea does not model. Group assignment, the empty-value group and both writes are the
// server's, so the page and the CLI cannot disagree about which group a card is in.
func Board(ctx *context.Context) {
	ctx.Data["Title"] = "Board"
	ctx.Data["PageIsPlanning"] = true
	ctx.Data["APIBase"] = setting.AppSubURL + planningv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	ctx.Data["Groupings"] = planning_service.Groupings
	hub_web.SetPageToken(ctx)
	ctx.HTML(http.StatusOK, tplBoard)
}

// Roadmap renders /planning/roadmap: one bar per issue with dependency arrows. It needs
// no Projects API, so it renders on a build whose board degrades. Every bar names the source
// of each endpoint, and an inferred end is drawn distinctly from a recorded one.
func Roadmap(ctx *context.Context) {
	ctx.Data["Title"] = "Roadmap"
	ctx.Data["PageIsPlanning"] = true
	ctx.Data["APIBase"] = setting.AppSubURL + planningv1.BasePath
	ctx.Data["AppSubURL"] = setting.AppSubURL
	hub_web.SetPageToken(ctx)
	ctx.HTML(http.StatusOK, tplRoadmap)
}
