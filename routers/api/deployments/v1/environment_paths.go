// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"
	"sort"
	"strconv"

	deployments_model "gitea.dev/models/deployments"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
)

// PathNodeChecks names one environment's pre-deployment checks that carry parameters —
// reviewers, prior deployment and releases only are policy fields already published on the
// Environment resource itself.
type PathNodeChecks struct {
	WaitMinutes            int                             `json:"wait_minutes"`
	DeployWindow           *deployments_model.DeployWindow `json:"deploy_window"`
	RequiredStatusContexts []string                        `json:"required_status_contexts"`
	ExclusiveLock          bool                            `json:"exclusive_lock"`
}

// PathNode is one environment in the promotion path graph.
type PathNode struct {
	Name        string         `json:"name"`
	DependsOn   []string       `json:"depends_on"`
	AutoPromote bool           `json:"auto_promote"`
	Checks      PathNodeChecks `json:"checks"`
}

// PathEdge is one dependency: From holds the release before To does, so an edge points in the
// direction a release is promoted.
type PathEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// PromotionPath is the repository's effective environment set as a graph.
type PromotionPath struct {
	Nodes []PathNode `json:"nodes"`
	Edges []PathEdge `json:"edges"`
}

var environmentPathsQueryParams = []hubapi.Param{
	{
		Name: "repo_id", In: "query", Type: "integer", Required: true,
		Description: "Repository id; 0 is the instance-wide default set.",
	},
}

func environmentPathsEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getEnvironmentPaths", Method: http.MethodGet, Path: "/environments/paths",
			Summary: "The repository's environment dependency graph",
			Description: "The effective environment set for repo_id — its own rows, falling back per name to the " +
				"instance-wide default set — as nodes and the depends_on edges between them. The path editor renders this.",
			Tag: "environments", QueryParams: environmentPathsQueryParams,
			CLINames: []string{"environment-paths"},
			Response: "PromotionPath", ResponseIs: "object",
		},
		Handler: GetEnvironmentPaths,
	}
}

// GetEnvironmentPaths answers GET /environments/paths.
func GetEnvironmentPaths(ctx *context.APIContext) {
	repoIDRaw := ctx.Req.URL.Query().Get("repo_id")
	repoID, err := strconv.ParseInt(repoIDRaw, 10, 64)
	if repoIDRaw == "" || err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_repo_id", "repo_id is not a number",
			"Call GET "+BasePath+"/environments/paths?repo_id=<id>; 0 is the instance-wide default set.")
		return
	}
	if repoID != deployments_model.DefaultsRepoID {
		visible, err := environmentIsVisible(ctx, repoID)
		if err != nil {
			ctx.APIErrorInternal(err)
			return
		}
		if !visible {
			hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
				"no repository with that id is visible to you",
				"Check repo_id and that your token's account can see the repository.")
			return
		}
	}

	names, err := deployments_model.EffectiveEnvironmentNames(ctx, repoID)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	envs := make([]*deployments_model.Environment, 0, len(names))
	for _, name := range names {
		env, err := deployments_model.GetEnvironment(ctx, repoID, name)
		if err != nil {
			continue // resolved a moment ago; a concurrent delete does not fail the whole graph
		}
		envs = append(envs, env)
	}
	sort.Slice(envs, func(i, j int) bool {
		if envs[i].SortOrder != envs[j].SortOrder {
			return envs[i].SortOrder < envs[j].SortOrder
		}
		return envs[i].Name < envs[j].Name
	})

	path := PromotionPath{Nodes: make([]PathNode, 0, len(envs)), Edges: []PathEdge{}}
	for _, env := range envs {
		path.Nodes = append(path.Nodes, PathNode{
			Name: env.Name, DependsOn: env.DependsOn, AutoPromote: env.AutoPromote,
			Checks: PathNodeChecks{
				WaitMinutes: env.WaitMinutes, DeployWindow: env.DeployWindow,
				RequiredStatusContexts: env.RequiredStatusContexts, ExclusiveLock: env.ExclusiveLock,
			},
		})
		for _, dep := range env.DependsOn {
			path.Edges = append(path.Edges, PathEdge{From: deployments_model.NormalizeEnvironmentName(dep), To: env.Name})
		}
	}
	ctx.JSON(http.StatusOK, path)
}
