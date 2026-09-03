// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package deployments renders the fork's environment, deployment, grid, review and CI
// overview pages. Each page is a client of a documented /api/deployments/v1 endpoint: the
// handler serves the page shell and nothing else, and every figure on the page arrives over
// the public API.
package deployments

import (
	hub_web "gitea.dev/routers/web/hub"
)

// RouteRegistrar is the slice of *web.Router this package needs.
type RouteRegistrar interface {
	Get(pattern string, h ...any)
}

// RegisterRoutes mounts the deployments pages. routers/web/hubroutes calls this from its own
// RegisterRoutes, behind hub's own settings gate.
func RegisterRoutes(m RouteRegistrar, reqSignIn any) {
	m.Get("/delivery/environments", reqSignIn, hub_web.DeploymentsPagesEnabled, Environment)
	m.Get("/delivery/environments/{name}", reqSignIn, hub_web.DeploymentsPagesEnabled, Environment)
	m.Get("/delivery/environments/{id}/edit", reqSignIn, hub_web.DeploymentsPagesEnabled, EnvironmentEdit)
	m.Get("/delivery/grid", reqSignIn, hub_web.DeploymentsPagesEnabled, Grid)
	m.Get("/delivery/ci", reqSignIn, hub_web.DeploymentsPagesEnabled, CI)
	m.Get("/delivery/promote", reqSignIn, hub_web.DeploymentsPagesEnabled, Promote)
	m.Get("/delivery/approvals", reqSignIn, hub_web.DeploymentsPagesEnabled, Approvals)
	m.Get("/delivery/environments/{name}/approvals", reqSignIn, hub_web.DeploymentsPagesEnabled, Approvals)
}
