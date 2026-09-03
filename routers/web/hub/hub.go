// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package hub is what routers/web/planning and routers/web/deployments share: the settings
// gate, the page token minted for the fork's own writes, and the route registrar both areas
// register into. Each page is a client of a documented API endpoint: the handler serves the
// page shell and nothing else, and every figure on the page arrives over the public API.
package hub

import (
	"net/http"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/modules/log"
	"gitea.dev/services/context"
	hub_service "gitea.dev/services/hub"
)

// PlanningPagesEnabled is the settings gate for routers/web/planning's routes, mirroring
// reqMilestonesDashboardPageEnabled so that area can be switched off on its own.
func PlanningPagesEnabled(ctx *context.Context) {
	if !hub_service.PlanningPagesEnabled() {
		ctx.HTTPError(http.StatusForbidden)
	}
}

// DeploymentsPagesEnabled is the settings gate for routers/web/deployments' routes.
func DeploymentsPagesEnabled(ctx *context.Context) {
	if !hub_service.DeploymentsPagesEnabled() {
		ctx.HTTPError(http.StatusForbidden)
	}
}

// SetPageToken mints a short-lived API token so a page's JS can call the fork's API without
// the user pasting a token manually. Reads already use the browser session; this covers
// writes only. The token is scoped to repository and issue writes.
func SetPageToken(ctx *context.Context) {
	if ctx.Doer == nil {
		return
	}
	t := &auth_model.AccessToken{
		UID:   ctx.Doer.ID,
		Name:  "_hub_page",
		Scope: auth_model.AccessTokenScopeWriteRepository + "," + auth_model.AccessTokenScopeWriteIssue,
	}
	if err := auth_model.NewAccessToken(ctx, t); err != nil {
		log.Error("hub: mint page token for %s: %v", ctx.Doer.Name, err)
		return
	}
	ctx.Data["PageToken"] = t.Token
}

// RouteRegistrar is the slice of *web.Router this package needs. Taking an interface keeps
// the registration testable without standing up a router.
type RouteRegistrar interface {
	Get(pattern string, h ...any)
}
