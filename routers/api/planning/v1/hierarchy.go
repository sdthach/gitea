// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"

	issues_model "gitea.dev/models/issues"
	"gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	planning_service "gitea.dev/services/planning"
)

func setIssueParentEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "setIssueParent", Method: http.MethodPut, Path: "/issues/{issue_id}/parent",
			Summary: "Set an issue's parent",
			Description: "Records the parent in plan_issue_parent, replacing the retired label convention. " +
				"Refused same_issue, cross_repo, pull_request (either side), untyped_issue (either side, naming " +
				"which), rank_mismatch (naming both type names and ranks), cycle (the parent sits in the child's " +
				"own subtree) or too_deep (past 8 levels). Authorized by Gitea's own write check on the Issues unit.",
			Tag: "issues", PathParams: issueParam,
			Body: append(append([]hubapi.Param{}, repoParam...),
				hubapi.Param{Name: "parent_issue_id", In: "body", Type: "integer", Required: true, Description: "The parent to link."}),
			CLINames: []string{"issue-set-parent"},
			Response: "IssueFacets", ResponseIs: "object",
		},
		Handler: SetIssueParentHandler,
	}
}

func clearIssueParentEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "clearIssueParent", Method: http.MethodDelete, Path: "/issues/{issue_id}/parent",
			Summary:     "Remove an issue's parent",
			Description: "Same authorization as setting it.",
			Tag:         "issues", PathParams: issueParam,
			Body:     append([]hubapi.Param{}, repoParam...),
			CLINames: []string{"issue-clear-parent"},
			Response: "IssueFacets", ResponseIs: "object",
		},
		Handler: ClearIssueParentHandler,
	}
}

// SetIssueParentHandler answers PUT /issues/{issue_id}/parent. issueTarget already resolves
// and authorizes the child issue exactly like every other issue-scoped write.
func SetIssueParentHandler(ctx *context.APIContext) {
	body, _, issue, ok := issueTarget(ctx)
	if !ok {
		return
	}
	parent, ok := readableParent(ctx, body.ParentIssueID)
	if !ok {
		return
	}
	if err := planning_service.SetIssueParent(ctx, issue, parent); err != nil {
		hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
		return
	}
	renderIssueFacets(ctx, issue)
}

// readableParent resolves the parent a body names, refusing parent_not_found — never
// cross_repo — for one the caller cannot read: answering cross_repo would confirm an id exists
// in a repository hidden from them, which parent_not_found never does.
func readableParent(ctx *context.APIContext, parentIssueID int64) (*issues_model.Issue, bool) {
	notFound := func() {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "parent_not_found",
			"no issue with that id exists",
			"Read the chart's rows from "+BasePath+"/roadmap and use one of their issue_id values.")
	}
	parent, err := issues_model.GetIssueByID(ctx, parentIssueID)
	if err != nil {
		notFound()
		return nil, false
	}
	repo, err := repo_model.GetRepositoryByID(ctx, parent.RepoID)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}
	if !perm.CanReadIssuesOrPulls(parent.IsPull) {
		notFound()
		return nil, false
	}
	return parent, true
}

// ClearIssueParentHandler answers DELETE /issues/{issue_id}/parent.
func ClearIssueParentHandler(ctx *context.APIContext) {
	_, _, issue, ok := issueTarget(ctx)
	if !ok {
		return
	}
	if err := planning_service.RemoveIssueParent(ctx, issue.ID); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	renderIssueFacets(ctx, issue)
}
