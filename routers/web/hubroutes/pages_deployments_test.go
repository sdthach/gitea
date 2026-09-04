// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hubroutes

import (
	hub_web "gitea.dev/routers/web/hub"
	"gitea.dev/services/context"
)

var deploymentsPages = []forkPage{
	{
		dir:      "deployments",
		template: "environment.tmpl",
		endpoints: []string{
			"/environments", "/environments/{id}", "/secret-scopes", "/secret-scopes/{id}",
			"/repos/{owner}/{repo}/environments/{name}/secrets",
		},
		fetch: "/environments?",
	},
	{
		dir:       "deployments",
		template:  "matrix.tmpl",
		endpoints: []string{"/deployments/matrix", "/deployments", "/repos/{owner}/{repo}/releases"},
		fetch:     "/deployments/matrix?",
	},
	{
		dir:       "deployments",
		template:  "insights.tmpl",
		endpoints: []string{"/insights", "/insights/trends", "/insights/repos", "/runs"},
		fetch:     "/insights?",
	},
	{
		dir:       "deployments",
		template:  "new.tmpl",
		endpoints: []string{"/deployments"},
		fetch:     "/deployments",
	},
	{
		dir:       "deployments",
		template:  "reviews.tmpl",
		endpoints: []string{"/reviews"},
		fetch:     "/reviews?",
	},
}

var deploymentsFragments = map[string]bool{
	"release_environments.tmpl": true,
}

var deploymentsGates = map[string]func(*context.Context){
	"/deployments/environments":                hub_web.DeploymentsPagesEnabled,
	"/deployments/environments/{name}":         hub_web.DeploymentsPagesEnabled,
	"/deployments/environments/{id}/edit":      hub_web.DeploymentsPagesEnabled,
	"/deployments":                             hub_web.DeploymentsPagesEnabled,
	"/deployments/insights":                    hub_web.DeploymentsPagesEnabled,
	"/deployments/new":                         hub_web.DeploymentsPagesEnabled,
	"/deployments/reviews":                     hub_web.DeploymentsPagesEnabled,
	"/deployments/environments/{name}/reviews": hub_web.DeploymentsPagesEnabled,
}

var deploymentsPatterns = []string{
	"/deployments/environments", "/deployments/environments/{name}",
	"/deployments/environments/{id}/edit", "/deployments",
	"/deployments/insights", "/deployments/new",
	"/deployments/reviews", "/deployments/environments/{name}/reviews",
}
