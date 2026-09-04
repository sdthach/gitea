// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package planning renders the fork's project page and its settings. The page is a client
// of the documented /api/planning/v1 endpoints: the handler serves the page shell and
// nothing else, and every figure on the page arrives over the public API.
package planning

import (
	hub_web "gitea.dev/routers/web/hub"
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
	m.Get("/planning/settings/{owner}", reqSignIn, hub_web.PlanningPagesEnabled, Settings)
	m.Get("/planning/settings/{owner}/{repo}", reqSignIn, hub_web.PlanningPagesEnabled, RepoSettings)
	m.Post("/planning/issues/{id}/schedule", reqSignIn, hub_web.PlanningPagesEnabled, ScheduleIssue)
	m.Post("/planning/issues/{id}/type", reqSignIn, hub_web.PlanningPagesEnabled, TypeIssue)
	m.Post("/planning/issues/{id}/parent", reqSignIn, hub_web.PlanningPagesEnabled, ParentIssue)
	m.Post("/planning/issues/{id}/fields", reqSignIn, hub_web.PlanningPagesEnabled, FieldsIssue)
	m.Post("/planning/issues/{id}/estimate", reqSignIn, hub_web.PlanningPagesEnabled, EstimateIssue)
	m.Post("/planning/milestones/{id}/schedule", reqSignIn, hub_web.PlanningPagesEnabled, ScheduleMilestone)
}
