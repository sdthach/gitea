// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"

	issues_model "gitea.dev/models/issues"
	"gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/json"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
)

// dependencyBody is what both dependency writes take: repo plus, for the add, the issue on the
// other end of the edge.
type dependencyBody struct {
	Repo             string `json:"repo"`
	DependsOnIssueID int64  `json:"depends_on_issue_id"`
}

func readDependencyBody(ctx *context.APIContext) (*dependencyBody, bool) {
	raw, ok := readBoundedBody(ctx)
	if !ok {
		return nil, false
	}
	body := new(dependencyBody)
	if err := json.Unmarshal(raw, body); err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"repo": "acme/widgets", "depends_on_issue_id": 12}.`)
		return nil, false
	}
	return body, true
}

// dependencyTarget resolves the repository and the issue a dependency write names, and answers
// the two refusals both writes share, in that order, before either touches anything: the
// repository's Issues unit must have dependencies turned on, and the caller must hold write
// access to it — checked against the path issue's own IsPull, exactly as Gitea's own dependency
// panel checks it.
func dependencyTarget(ctx *context.APIContext) (*dependencyBody, *repo_model.Repository, *issues_model.Issue, bool) {
	body, ok := readDependencyBody(ctx)
	if !ok {
		return nil, nil, nil, false
	}
	repo, perm, ok := readableRepo(ctx, body.Repo)
	if !ok {
		return nil, nil, nil, false
	}
	issue, err := issues_model.GetIssueByID(ctx, ctx.PathParamInt64("issue_id"))
	if err != nil || issue.RepoID != repo.ID {
		hubapi.APIError(ctx, http.StatusNotFound, "issue_not_found",
			"no issue with that id belongs to "+repo.FullName(),
			"The path takes the issue's global id, not its per-repository number; "+BasePath+"/roadmap publishes issue_id on every bar.")
		return nil, nil, nil, false
	}
	// The write check runs before dependencies_disabled: a refusal must not reveal whether a
	// unit is turned on to a caller who could not write it anyway.
	if !perm.CanWriteIssuesOrPulls(issue.IsPull) {
		hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
			"your account has no write access to the Issues unit of "+repo.FullName(),
			"Ask a repository administrator for write permission on Issues.")
		return nil, nil, nil, false
	}
	if !repo.IsDependenciesEnabled(ctx) {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "dependencies_disabled",
			"dependencies are turned off for the Issues unit of "+repo.FullName(),
			"Turn on dependencies in the repository's issue settings first.")
		return nil, nil, nil, false
	}
	return body, repo, issue, true
}

// dependencyUnit is the unit an issue's own readability is checked against: pull requests and
// issues are gated separately, and a dependency can point at either.
func dependencyUnit(isPull bool) unit.Type {
	if isPull {
		return unit.TypePullRequests
	}
	return unit.TypeIssues
}

var dependencyPathParams = append(append([]hubapi.Param{}, issueParam...),
	hubapi.Param{Name: "dependency_id", In: "path", Type: "integer", Required: true, Description: "The depends-on issue's global id."})

func addIssueDependencyEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "addIssueDependency", Method: http.MethodPost, Path: "/issues/{issue_id}/dependencies",
			Summary: "Block an issue on another, drawn as the roadmap's arrow",
			Description: "Records that issue_id is blocked by depends_on_issue_id through Gitea's own " +
				"CreateIssueDependency, which leaves the same comment Gitea's own dependency panel leaves. " +
				"Refused dependencies_disabled when the repository's Issues unit has dependencies turned off, " +
				"same_issue for an issue naming itself, cross_repo when the two issues do not share a repository, " +
				"dependency_exists for a pair already linked, and circular_dependency for a pair that would " +
				"block each other. depends_on_issue_id missing or unreadable to the caller answers " +
				"dependency_not_found rather than cross_repo, never revealing an issue outside the caller's " +
				"reach. Authorized by Gitea's own write check on the Issues unit.",
			Tag: "roadmap", PathParams: issueParam,
			Body: append(append([]hubapi.Param{}, repoParam...),
				hubapi.Param{Name: "depends_on_issue_id", In: "body", Type: "integer", Required: true, Description: "The issue this one is blocked by."}),
			CLINames: []string{"issue-add-dependency"},
			Response: "Roadmap", ResponseIs: "object",
		},
		Handler: AddIssueDependency,
	}
}

func removeIssueDependencyEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "removeIssueDependency", Method: http.MethodDelete, Path: "/issues/{issue_id}/dependencies/{dependency_id}",
			Summary: "Remove a blocking dependency",
			Description: "Removes the dependency_id edge through Gitea's own RemoveIssueDependency, which " +
				"leaves the same comment Gitea's own dependency panel leaves. A pair not linked answers " +
				"dependency_not_found. Authorized by Gitea's own write check on the Issues unit.",
			Tag: "roadmap", PathParams: dependencyPathParams,
			Body:     repoParam,
			CLINames: []string{"issue-remove-dependency"},
			Response: "Roadmap", ResponseIs: "object",
		},
		Handler: RemoveIssueDependency,
	}
}

// AddIssueDependency answers POST /issues/{issue_id}/dependencies.
func AddIssueDependency(ctx *context.APIContext) {
	body, repo, issue, ok := dependencyTarget(ctx)
	if !ok {
		return
	}

	dep, err := issues_model.GetIssueByID(ctx, body.DependsOnIssueID)
	if err != nil {
		dependencyNotFound(ctx)
		return
	}
	depRepo := repo
	if dep.RepoID != repo.ID {
		if depRepo, err = repo_model.GetRepositoryByID(ctx, dep.RepoID); err != nil {
			ctx.APIErrorInternal(err)
			return
		}
	}
	// Readability is checked before cross_repo so an issue outside the caller's reach in
	// another repository is never distinguished from one that does not exist.
	if !access.CheckRepoUnitUser(ctx, depRepo, ctx.Doer, dependencyUnit(dep.IsPull)) {
		dependencyNotFound(ctx)
		return
	}
	if dep.RepoID != repo.ID {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "cross_repo",
			"the two issues must belong to the same repository",
			"Choose a dependency from "+repo.FullName()+".")
		return
	}
	if dep.ID == issue.ID {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "same_issue",
			"an issue cannot depend on itself",
			"Choose a different issue as the dependency.")
		return
	}

	if err := issues_model.CreateIssueDependency(ctx, ctx.Doer, issue, dep); err != nil {
		switch {
		case issues_model.IsErrDependencyExists(err):
			hubapi.APIError(ctx, http.StatusUnprocessableEntity, "dependency_exists",
				"that dependency is already recorded",
				"Read the chart's arrows from "+BasePath+"/roadmap; it is already drawn.")
		case issues_model.IsErrCircularDependency(err):
			hubapi.APIError(ctx, http.StatusUnprocessableEntity, "circular_dependency",
				"that pair would block each other",
				"Choose a dependency that does not already depend on this issue.")
		default:
			ctx.APIErrorInternal(err)
		}
		return
	}
	renderRoadmapAfterWrite(ctx, repo, roadmapView{})
}

func dependencyNotFound(ctx *context.APIContext) {
	hubapi.APIError(ctx, http.StatusUnprocessableEntity, "dependency_not_found",
		"no issue with that id is visible to you",
		"Read the chart's rows from "+BasePath+"/roadmap and use one of their issue_id values.")
}

// RemoveIssueDependency answers DELETE /issues/{issue_id}/dependencies/{dependency_id}.
func RemoveIssueDependency(ctx *context.APIContext) {
	_, repo, issue, ok := dependencyTarget(ctx)
	if !ok {
		return
	}
	dep, err := issues_model.GetIssueByID(ctx, ctx.PathParamInt64("dependency_id"))
	if err != nil {
		hubapi.APIError(ctx, http.StatusNotFound, "dependency_not_found",
			"no dependency with that id is recorded on this issue",
			"Read the chart's arrows from "+BasePath+"/roadmap and use one of their to_issue_id values.")
		return
	}
	if dep.RepoID != repo.ID {
		depRepo, err := repo_model.GetRepositoryByID(ctx, dep.RepoID)
		if err != nil {
			ctx.APIErrorInternal(err)
			return
		}
		// Readability is checked before removing anything, exactly as the add path checks it,
		// so a dependency in a repository outside the caller's reach is never distinguished
		// from one that does not exist.
		if !access.CheckRepoUnitUser(ctx, depRepo, ctx.Doer, dependencyUnit(dep.IsPull)) {
			hubapi.APIError(ctx, http.StatusNotFound, "dependency_not_found",
				"no dependency with that id is recorded on this issue",
				"Read the chart's arrows from "+BasePath+"/roadmap and use one of their to_issue_id values.")
			return
		}
	}
	if err := issues_model.RemoveIssueDependency(ctx, ctx.Doer, issue, dep, issues_model.DependencyTypeBlockedBy); err != nil {
		if issues_model.IsErrDependencyNotExists(err) {
			hubapi.APIError(ctx, http.StatusNotFound, "dependency_not_found",
				"no dependency with that id is recorded on this issue",
				"Read the chart's arrows from "+BasePath+"/roadmap and use one of their to_issue_id values.")
			return
		}
		ctx.APIErrorInternal(err)
		return
	}
	renderRoadmapAfterWrite(ctx, repo, roadmapView{})
}
