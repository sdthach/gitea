// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hubroutes

import (
	hub_web "gitea.dev/routers/web/hub"
	"gitea.dev/services/context"
)

var planningPages = []forkPage{
	{
		dir:      "planning",
		template: "project.tmpl",
		endpoints: []string{
			"/board", "/roadmap", "/roadmap/capacity", "/projects", "/projects/{project_id}/views",
			"/projects/{project_id}/views/{view_id}",
			"/issues", "/issues/{issue_id}/milestone", "/issues/{issue_id}/dates",
			"/issues/{issue_id}/type", "/issues/{issue_id}/fields",
			"/issues/{issue_id}/estimate", "/issues/{issue_id}/group", "/issues/{issue_id}/parent",
			"/issues/{issue_id}/dependencies", "/issues/{issue_id}/dependencies/{dependency_id}",
			"/board/cards", "/board/cards/{issue_id}/column", "/board/cards/{issue_id}/group",
			"/board/columns/{column_id}/order", "/milestones/{milestone_id}/schedule",
		},
		fetch:  "/board",
		client: "web_src/js/features/planning/api.ts",
	},
	{
		dir:      "planning",
		template: "settings.tmpl",
		endpoints: []string{
			"/issue-types", "/issue-types/{id}", "/fields", "/fields/{id}", "/capacity", "/capacity/{user_id}",
		},
		fetch:  "/issue-types",
		client: "web_src/js/features/planning/api.ts",
	},
}

var planningFragments = map[string]bool{
	"swimlanes.tmpl":       true,
	"issue_sidebar.tmpl":   true,
	"issue_type_icon.tmpl": true,
	"milestone_start.tmpl": true,
}

var planningGates = map[string]func(*context.Context){
	"/planning/projects":                             hub_web.PlanningPagesEnabled,
	"/planning/projects/{owner}/{repo}/{project_id}": hub_web.PlanningPagesEnabled,
	"/planning/settings/{owner}":                     hub_web.PlanningPagesEnabled,
	"/planning/settings/{owner}/{repo}":              hub_web.PlanningPagesEnabled,
	"/planning/issues/{id}/schedule":                 hub_web.PlanningPagesEnabled,
	"/planning/issues/{id}/type":                     hub_web.PlanningPagesEnabled,
	"/planning/issues/{id}/parent":                   hub_web.PlanningPagesEnabled,
	"/planning/issues/{id}/fields":                   hub_web.PlanningPagesEnabled,
	"/planning/issues/{id}/estimate":                 hub_web.PlanningPagesEnabled,
	"/planning/milestones/{id}/schedule":             hub_web.PlanningPagesEnabled,
}

var planningPatterns = []string{
	"/planning/projects", "/planning/projects/{owner}/{repo}/{project_id}",
	"/planning/settings/{owner}", "/planning/settings/{owner}/{repo}",
	"/planning/issues/{id}/schedule", "/planning/issues/{id}/type", "/planning/issues/{id}/parent",
	"/planning/issues/{id}/fields", "/planning/issues/{id}/estimate", "/planning/milestones/{id}/schedule",
}
