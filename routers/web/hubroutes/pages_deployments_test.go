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
			"/environments", "/environments/{id}", "/environments/paths", "/secret-scopes", "/secret-scopes/{id}",
			"/repos/{owner}/{repo}/environments/{name}/secrets",
		},
		fetch:     "/environments",
		client:    "web_src/js/features/deployments/api.ts",
		component: "web_src/js/features/deployments/EnvironmentsPage.vue",
		calls:     []string{"getEnvironments", "createEnvironment"},
	},
	{
		dir:      "deployments",
		template: "matrix.tmpl",
		endpoints: []string{
			"/deployments/matrix", "/deployments", "/repos/{owner}/{repo}/releases",
			"/deployments/{id}/checks", "/reviews", "/reviews/{id}/approve", "/reviews/{id}/reject",
		},
		fetch:     "/deployments/matrix",
		client:    "web_src/js/features/deployments/api.ts",
		component: "web_src/js/features/deployments/MatrixPage.vue",
		calls: []string{
			"getMatrix", "getDeploymentHistory", "getDeploymentChecks", "getReleases",
			"getReviews", "approveReview", "rejectReview",
		},
	},
	{
		dir:       "deployments",
		template:  "insights.tmpl",
		endpoints: []string{"/insights", "/insights/trends", "/insights/repos", "/runs"},
		fetch:     "/insights",
		client:    "web_src/js/features/deployments/api.ts",
		component: "web_src/js/features/deployments/InsightsPage.vue",
		calls:     []string{"getInsights", "getInsightsTrends", "getInsightsRepos", "getRuns"},
	},
	{
		dir:       "deployments",
		template:  "new.tmpl",
		endpoints: []string{"/deployments"},
		fetch:     "/deployments",
		client:    "web_src/js/features/deployments/api.ts",
		component: "web_src/js/features/deployments/NewPage.vue",
		calls:     []string{"planOrConfirmDeployment"},
	},
	{
		dir:      "deployments",
		template: "reviews.tmpl",
		endpoints: []string{
			"/reviews", "/reviews/{id}/approve", "/reviews/{id}/reject", "/deployments", "/deployments/{id}/checks",
		},
		fetch:     "/reviews",
		client:    "web_src/js/features/deployments/api.ts",
		component: "web_src/js/features/deployments/ReviewsPage.vue",
		calls:     []string{"getReviews", "getWaitingDeployments", "getDeploymentChecks", "approveReview", "rejectReview"},
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
