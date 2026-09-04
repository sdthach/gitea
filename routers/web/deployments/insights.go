// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"net/http"

	"gitea.dev/modules/setting"
	"gitea.dev/modules/templates"
	deploymentsv1 "gitea.dev/routers/api/deployments/v1"
	"gitea.dev/services/context"
	deployments_service "gitea.dev/services/deployments"
)

const tplInsights templates.TplName = "deployments/insights"

// Insights renders /deployments/insights, the cross-repository CI/CD overview.
//
// Like every other fork page it is a client of documented endpoints and reads nothing the API
// does not serve: the handler passes the API base and the default window and
// nothing else. Every per-run, per-workflow and per-repository detail on it links out to
// Gitea's own page rather than to a reimplementation.
func Insights(ctx *context.Context) {
	ctx.Data["Title"] = "Insights"
	ctx.Data["PageIsDeployments"] = true
	ctx.PageData["deploymentsInsights"] = map[string]any{
		"apiBase":           setting.AppSubURL + deploymentsv1.BasePath,
		"appSubUrl":         setting.AppSubURL,
		"defaultWindowDays": deployments_service.DefaultWindowDays,
	}
	ctx.HTML(http.StatusOK, tplInsights)
}
