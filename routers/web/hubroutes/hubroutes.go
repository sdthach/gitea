// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package hubroutes composes routers/web/planning and routers/web/deployments behind
// routers/web/hub's settings gate. It exists apart from hub itself so that both area
// packages can import hub directly for the gate and the page token without a cycle.
package hubroutes

import (
	deployments_web "gitea.dev/routers/web/deployments"
	hub_web "gitea.dev/routers/web/hub"
	planning_web "gitea.dev/routers/web/planning"
)

// RegisterRoutes mounts the fork's pages. routers/web/web.go inserts one call to it beside
// /milestones. Each page sits behind reqSignIn and the settings gate.
func RegisterRoutes(m hub_web.RouteRegistrar, reqSignIn any) {
	deployments_web.RegisterRoutes(m, reqSignIn)
	planning_web.RegisterRoutes(m, reqSignIn)
}
