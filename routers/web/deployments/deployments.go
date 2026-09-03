// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package deployments renders the fork's environment, deployment, matrix, review and insights
// pages. Each page is a client of a documented /api/deployments/v1 endpoint: the handler
// serves the page shell and nothing else, and every figure on the page arrives over the
// public API.
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
	m.Get("/deployments/environments", reqSignIn, hub_web.DeploymentsPagesEnabled, Environment)
	m.Get("/deployments/environments/{name}", reqSignIn, hub_web.DeploymentsPagesEnabled, Environment)
	m.Get("/deployments/environments/{id}/edit", reqSignIn, hub_web.DeploymentsPagesEnabled, EnvironmentEdit)
	m.Get("/deployments", reqSignIn, hub_web.DeploymentsPagesEnabled, Matrix)
	m.Get("/deployments/insights", reqSignIn, hub_web.DeploymentsPagesEnabled, Insights)
	m.Get("/deployments/new", reqSignIn, hub_web.DeploymentsPagesEnabled, New)
	m.Get("/deployments/reviews", reqSignIn, hub_web.DeploymentsPagesEnabled, Reviews)
	m.Get("/deployments/environments/{name}/reviews", reqSignIn, hub_web.DeploymentsPagesEnabled, Reviews)
}
