// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hubroutes

import (
	"gitea.dev/modules/setting"
	hub_web "gitea.dev/routers/web/hub"
	"gitea.dev/services/context"
)

// redirectTable is every /delivery/* URL the fork used to serve, mapped to where its page
// moved. It is the one place that mapping is written down, so an old bookmark or a link
// still out there in an issue comment keeps working rather than 404ing.
var redirectTable = []struct {
	from string
	to   func(ctx *context.Context) string
}{
	{"/delivery/environments", func(*context.Context) string { return "/deployments/environments" }},
	{"/delivery/environments/{name}", func(ctx *context.Context) string {
		return "/deployments/environments/" + ctx.PathParam("name")
	}},
	{"/delivery/environments/{id}/edit", func(ctx *context.Context) string {
		return "/deployments/environments/" + ctx.PathParam("id") + "/edit"
	}},
	{"/delivery/environments/{name}/approvals", func(ctx *context.Context) string {
		return "/deployments/environments/" + ctx.PathParam("name") + "/reviews"
	}},
	{"/delivery/grid", func(*context.Context) string { return "/deployments" }},
	{"/delivery/promote", func(*context.Context) string { return "/deployments/new" }},
	{"/delivery/approvals", func(*context.Context) string { return "/deployments/reviews" }},
	{"/delivery/ci", func(*context.Context) string { return "/deployments/insights" }},
	{"/delivery/board", func(*context.Context) string { return "/planning/board" }},
	{"/delivery/timeline", func(*context.Context) string { return "/planning/roadmap" }},
}

// registerRedirects mounts every entry of redirectTable as a 303 to its replacement, query
// string carried over unchanged.
func registerRedirects(m hub_web.RouteRegistrar) {
	for _, r := range redirectTable {
		to := r.to
		m.Get(r.from, func(ctx *context.Context) {
			dest := setting.AppSubURL + to(ctx)
			if q := ctx.Req.URL.RawQuery; q != "" {
				dest += "?" + q
			}
			ctx.Redirect(dest)
		})
	}
}
