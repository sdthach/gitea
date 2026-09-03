// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	issues_model "gitea.dev/models/issues"
	org_model "gitea.dev/models/organization"
	"gitea.dev/models/perm/access"
	planning_model "gitea.dev/models/planning"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/json"
	"gitea.dev/modules/svg"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	"gitea.dev/services/hub/query"
	planning_service "gitea.dev/services/planning"
)

// maxIssueTypeIDs caps how many issue ids one assignment-batch request may name.
const maxIssueTypeIDs = 200

// IssueTypeRow is one type as the CRUD endpoints publish it: the row plus the scope it lives
// in, so a client can show where an edit would apply.
type IssueTypeRow struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Color   string `json:"color"`
	Icon    string `json:"icon"`
	Rank    int    `json:"rank"`
	Sort    int    `json:"sort"`
	Scope   string `json:"scope"`
	ScopeID int64  `json:"scope_id"`
}

// IssueTypeAssignmentRow is one issue's assignment, joined with the type it names, the shape
// GET /issue-type-assignments answers with.
type IssueTypeAssignmentRow struct {
	IssueID int64  `json:"issue_id"`
	TypeID  int64  `json:"type_id"`
	Name    string `json:"name"`
	Color   string `json:"color"`
	Icon    string `json:"icon"`
	IconSVG string `json:"icon_svg"`
}

func issueTypeRowFrom(row *planning_model.IssueType) IssueTypeRow {
	scope, scopeID := planning_service.ScopeInstance, int64(0)
	switch {
	case row.RepoID > 0:
		scope, scopeID = planning_service.ScopeRepo, row.RepoID
	case row.OrgID > 0:
		scope, scopeID = planning_service.ScopeOrg, row.OrgID
	}
	return IssueTypeRow{
		ID: row.ID, Name: row.Name, Color: row.Color, Icon: row.Icon, Rank: row.Rank, Sort: row.Sort,
		Scope: scope, ScopeID: scopeID,
	}
}

var issueTypeSpec = query.Spec{
	Resource: "issue-types",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "org_id", Column: "org_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
	},
	PrimaryKey: "id",
	Paging:     query.PagingOffset,
}

var issueTypeAssignmentSpec = query.Spec{
	Resource: "issue-type-assignments",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "issue_ids", Column: "issue_ids", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
	},
	PrimaryKey: "issue_id",
	Paging:     query.PagingOffset,
}

var issueTypeIDParam = []hubapi.Param{
	{Name: "id", In: "path", Type: "integer", Required: true, Description: "The type's id."},
}

var issueTypeBodyParams = []hubapi.Param{
	{Name: "repo_id", In: "body", Type: "integer", Description: "Scope the type to this repository. Mutually exclusive with org_id; both zero is the instance scope."},
	{Name: "org_id", In: "body", Type: "integer", Description: "Scope the type to this organization. Mutually exclusive with repo_id."},
	{Name: "name", In: "body", Type: "string", Required: true, Description: "1-50 characters, lower-cased on write; letters, digits, spaces, underscores and hyphens."},
	{Name: "color", In: "body", Type: "string", Required: true, Description: "The type's colour."},
	{Name: "icon", In: "body", Type: "string", Required: true, Description: "An octicon-* name shipped under public/assets/img/svg, such as octicon-issue-opened."},
	{Name: "rank", In: "body", Type: "integer", Required: true, Description: "1 (highest) to 9 (lowest)."},
}

func getIssueTypesEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getIssueTypes", Method: http.MethodGet, Path: "/issue-types",
			Summary: "The types visible from a repository or an organization",
			Description: "repo_id reads a repository's own types, its organization's when it has one, and the " +
				"instance's, nearest scope shadowing by name. org_id reads an organization's own types and the " +
				"instance's. Neither reads the instance scope alone. Readable by anyone who can read the named " +
				"repository's Issues unit, or to whom the named organization is visible.",
			Tag: "issue-types", Query: &issueTypeSpec, Response: "IssueType", ResponseIs: "array",
		},
		Handler: GetIssueTypes,
	}
}

func createIssueTypeEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "createIssueType", Method: http.MethodPost, Path: "/issue-types",
			Summary: "Create a type",
			Description: "Scope admin: a repository administrator for repo_id, an organization owner for org_id, " +
				"a site administrator for the instance scope (neither given). Refused bad_scope, bad_type_name, " +
				"bad_icon, bad_rank or type_exists.",
			Tag: "issue-types", Body: issueTypeBodyParams,
			CLINames: []string{"issue-type-create"},
			Response: "IssueType", ResponseIs: "object",
		},
		Handler: CreateIssueType,
	}
}

func updateIssueTypeEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "updateIssueType", Method: http.MethodPut, Path: "/issue-types/{id}",
			Summary:     "Update a type",
			Description: "Same scope admin check as creating one. Scope itself is fixed at creation and cannot be changed here.",
			Tag:         "issue-types", PathParams: issueTypeIDParam, Body: issueTypeBodyParams,
			CLINames: []string{"issue-type-update"},
			Response: "IssueType", ResponseIs: "object",
		},
		Handler: UpdateIssueType,
	}
}

func deleteIssueTypeEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "deleteIssueType", Method: http.MethodDelete, Path: "/issue-types/{id}",
			Summary: "Delete a type",
			Description: "Refused type_in_use, carrying the assignment count, unless force is true — which deletes the " +
				"type and clears every assignment naming it. Answers with the row as it stood just before deletion.",
			Tag: "issue-types", PathParams: issueTypeIDParam,
			Body:     []hubapi.Param{{Name: "force", In: "body", Type: "boolean", Description: "Delete a type still in use, clearing its assignments."}},
			CLINames: []string{"issue-type-delete"},
			Response: "IssueType", ResponseIs: "object",
		},
		Handler: DeleteIssueType,
	}
}

func setIssueTypeEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "setIssueType", Method: http.MethodPut, Path: "/issues/{issue_id}/type",
			Summary:     "Assign an issue's type",
			Description: "Refused type_not_visible when type_id names a type not visible from the issue's own repository. Authorized by Gitea's own write check on the Issues unit.",
			Tag:         "issues", PathParams: issueParam,
			Body: append(append([]hubapi.Param{}, repoParam...),
				hubapi.Param{Name: "type_id", In: "body", Type: "integer", Required: true, Description: "The type to assign."}),
			CLINames: []string{"issue-set-type"},
			Response: "IssueFacets", ResponseIs: "object",
		},
		Handler: SetIssueTypeHandler,
	}
}

func clearIssueTypeEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "clearIssueType", Method: http.MethodDelete, Path: "/issues/{issue_id}/type",
			Summary:     "Remove an issue's type",
			Description: "Same authorization as assigning one.",
			Tag:         "issues", PathParams: issueParam,
			Body:     append([]hubapi.Param{}, repoParam...),
			CLINames: []string{"issue-clear-type"},
			Response: "IssueFacets", ResponseIs: "object",
		},
		Handler: ClearIssueTypeHandler,
	}
}

func getIssueTypeAssignmentsEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getIssueTypeAssignments", Method: http.MethodGet, Path: "/issue-type-assignments",
			Summary: "Batch-read issues' assigned types",
			Description: "repo_id is required, and readable the same way GET /issue-types checks it; issue_ids " +
				"outside that repository are silently dropped rather than erroring. issue_ids is a comma-separated " +
				"list of global issue ids, at most 200 — more is refused too_many_ids. icon_svg is the rendered svg, " +
				"so a client needs no icon registry of its own.",
			Tag: "issue-types", Query: &issueTypeAssignmentSpec, Response: "IssueTypeAssignment", ResponseIs: "array",
		},
		Handler: GetIssueTypeAssignments,
	}
}

// issueTypeReadableRepo resolves and authorizes the repository a type read names.
func issueTypeReadableRepo(ctx *context.APIContext, repoID int64) (*repo_model.Repository, bool) {
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository with that id is visible to you", "Check the id against "+BasePath+"/repos.")
		return nil, false
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}
	if !perm.CanRead(unit.TypeIssues) {
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository with that id is visible to you", "Check the id against "+BasePath+"/repos.")
		return nil, false
	}
	return repo, true
}

// issueIDsInRepo keeps, in order, the ids among ids that belong to repoID; an id that does not
// exist or belongs elsewhere is dropped rather than erroring, since a batch read is not the
// place to disclose another repository's issue.
func issueIDsInRepo(ctx *context.APIContext, ids []int64, repoID int64) ([]int64, error) {
	if len(ids) == 0 {
		return ids, nil
	}
	issues, err := issues_model.GetIssuesByIDs(ctx, ids, true)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(issues))
	for _, issue := range issues {
		if issue.RepoID == repoID {
			out = append(out, issue.ID)
		}
	}
	return out, nil
}

// issueTypeVisibleOrg resolves an organization a type read names, 404 when it is not visible
// to the caller — an organization's existence is not disclosed to someone who cannot see it.
func issueTypeVisibleOrg(ctx *context.APIContext, orgID int64) (*org_model.Organization, bool) {
	org, err := org_model.GetOrgByID(ctx, orgID)
	if err != nil {
		hubapi.APIError(ctx, http.StatusNotFound, "org_not_found",
			"no organization with that id is visible to you",
			"Check the id against the organizations you belong to.")
		return nil, false
	}
	if !org_model.HasOrgOrUserVisible(ctx, org.AsUser(), ctx.Doer) {
		hubapi.APIError(ctx, http.StatusNotFound, "org_not_found",
			"no organization with that id is visible to you",
			"Check the id against the organizations you belong to.")
		return nil, false
	}
	return org, true
}

// GetIssueTypes answers GET /issue-types.
func GetIssueTypes(ctx *context.APIContext) {
	scopeListRead(ctx, issueTypeSpec, issueTypeReadableRepo, planning_service.TypesFor, planning_service.TypesForOrg)
}

type issueTypeBody struct {
	RepoID int64  `json:"repo_id"`
	OrgID  int64  `json:"org_id"`
	Name   string `json:"name"`
	Color  string `json:"color"`
	Icon   string `json:"icon"`
	Rank   int    `json:"rank"`
}

func readIssueTypeBody(ctx *context.APIContext) (*issueTypeBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(ctx.Req.Body, maxWriteBody+1))
	if err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "unreadable_body",
			"the request body could not be read", "Retry the request.")
		return nil, false
	}
	if len(raw) > maxWriteBody {
		hubapi.APIError(ctx, http.StatusBadRequest, "body_too_large",
			"the request body is larger than 16KiB", "A type is a handful of short fields; send only those.")
		return nil, false
	}
	body := new(issueTypeBody)
	if err := json.Unmarshal(raw, body); err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"repo_id": 1, "name": "bug", "color": "#d1242f", "icon": "octicon-bug", "rank": 3}.`)
		return nil, false
	}
	return body, true
}

// CreateIssueType answers POST /issue-types.
func CreateIssueType(ctx *context.APIContext) {
	body, ok := readIssueTypeBody(ctx)
	if !ok {
		return
	}
	row, err := planning_service.CreateType(ctx, ctx.Doer,
		planning_service.Scope{RepoID: body.RepoID, OrgID: body.OrgID},
		planning_service.TypeInput{Name: body.Name, Color: body.Color, Icon: body.Icon, Rank: body.Rank})
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
		return
	}
	ctx.JSON(http.StatusOK, issueTypeRowFrom(row))
}

// UpdateIssueType answers PUT /issue-types/{id}.
func UpdateIssueType(ctx *context.APIContext) {
	body, ok := readIssueTypeBody(ctx)
	if !ok {
		return
	}
	row, err := planning_service.UpdateType(ctx, ctx.Doer, ctx.PathParamInt64("id"),
		planning_service.TypeInput{Name: body.Name, Color: body.Color, Icon: body.Icon, Rank: body.Rank})
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
		return
	}
	ctx.JSON(http.StatusOK, issueTypeRowFrom(row))
}

// deleteIssueTypeBody is DELETE /issue-types/{id}'s only field; an absent or empty body means
// force is false.
type deleteIssueTypeBody struct {
	Force bool `json:"force"`
}

// readDeleteIssueTypeBody accepts an empty body as force=false, since most deletes name no
// flag at all.
func readDeleteIssueTypeBody(ctx *context.APIContext) (*deleteIssueTypeBody, bool) {
	if ctx.Req.Body == nil {
		return new(deleteIssueTypeBody), true
	}
	raw, err := io.ReadAll(io.LimitReader(ctx.Req.Body, maxWriteBody+1))
	if err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "unreadable_body",
			"the request body could not be read", "Retry the request.")
		return nil, false
	}
	body := new(deleteIssueTypeBody)
	if len(strings.TrimSpace(string(raw))) == 0 {
		return body, true
	}
	if err := json.Unmarshal(raw, body); err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {} to delete only when unused, or {"force": true} to clear its assignments too.`)
		return nil, false
	}
	return body, true
}

// DeleteIssueType answers DELETE /issue-types/{id}.
func DeleteIssueType(ctx *context.APIContext) {
	body, ok := readDeleteIssueTypeBody(ctx)
	if !ok {
		return
	}
	row, err := planning_service.DeleteType(ctx, ctx.Doer, ctx.PathParamInt64("id"), body.Force)
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusConflict, err)
		return
	}
	ctx.JSON(http.StatusOK, issueTypeRowFrom(row))
}

// SetIssueTypeHandler answers PUT /issues/{issue_id}/type.
func SetIssueTypeHandler(ctx *context.APIContext) {
	body, _, issue, ok := issueTarget(ctx)
	if !ok {
		return
	}
	if err := planning_service.SetIssueType(ctx, issue, body.TypeID); err != nil {
		hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
		return
	}
	renderIssueFacets(ctx, issue)
}

// ClearIssueTypeHandler answers DELETE /issues/{issue_id}/type.
func ClearIssueTypeHandler(ctx *context.APIContext) {
	_, _, issue, ok := issueTarget(ctx)
	if !ok {
		return
	}
	if err := planning_service.ClearIssueType(ctx, issue.ID); err != nil {
		hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
		return
	}
	renderIssueFacets(ctx, issue)
}

// applyGroupType resolves write.TypeName to a visible type and assigns it, or clears the
// issue's type when write.TypeName is empty — the empty-value group. issue.Repo must already
// be loaded.
func applyGroupType(ctx *context.APIContext, issue *issues_model.Issue, write planning_service.GroupWrite) bool {
	if write.TypeName == "" {
		if err := planning_service.ClearIssueType(ctx, issue.ID); err != nil {
			ctx.APIErrorInternal(err)
			return false
		}
		return true
	}
	types, err := planning_service.TypesFor(ctx, issue.Repo)
	if err != nil {
		ctx.APIErrorInternal(err)
		return false
	}
	for _, t := range types {
		if t.Name == write.TypeName {
			if err := planning_service.SetIssueType(ctx, issue, t.ID); err != nil {
				hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
				return false
			}
			return true
		}
	}
	hubapi.APIError(ctx, http.StatusUnprocessableEntity, "type_not_visible",
		"no type named "+write.TypeName+" is visible from "+issue.Repo.FullName(),
		"Create the type first, or move to one of the names GET "+BasePath+"/issue-types?repo_id="+
			strconv.FormatInt(issue.RepoID, 10)+" returns.")
	return false
}

// GetIssueTypeAssignments answers GET /issue-type-assignments.
func GetIssueTypeAssignments(ctx *context.APIContext) {
	q, ok := hubapi.ParseQuery(ctx, issueTypeAssignmentSpec)
	if !ok {
		return
	}
	repoID := hubapi.EqualityFilterInt(q, "repo_id")
	if repoID == 0 {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "missing_repo_id",
			"repo_id is required", "Send repo_id naming the repository the issue ids belong to.")
		return
	}
	repo, ok := issueTypeReadableRepo(ctx, repoID)
	if !ok {
		return
	}

	raw := strings.TrimSpace(hubapi.EqualityFilterString(q, "issue_ids"))
	ids := make([]int64, 0, 8)
	if raw != "" {
		for part := range strings.SplitSeq(raw, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil {
				hubapi.APIError(ctx, http.StatusBadRequest, "bad_issue_ids",
					fmt.Sprintf("%q is not a comma-separated list of issue ids", raw),
					"Send issue_ids as a comma-separated list of global issue ids.")
				return
			}
			ids = append(ids, id)
		}
	}
	if len(ids) > maxIssueTypeIDs {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "too_many_ids",
			fmt.Sprintf("%d issue ids were sent; at most %d are read in one request", len(ids), maxIssueTypeIDs),
			"Split the request into pages of at most 200 issue ids.")
		return
	}

	ids, err := issueIDsInRepo(ctx, ids, repo.ID)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	assigned, err := planning_service.Assignments(ctx, ids)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	out := make([]IssueTypeAssignmentRow, 0, len(assigned))
	for _, id := range ids {
		at, ok := assigned[id]
		if !ok {
			continue
		}
		out = append(out, IssueTypeAssignmentRow{
			IssueID: id, TypeID: at.TypeID, Name: at.Name, Color: at.Color, Icon: at.Icon,
			IconSVG: string(svg.RenderHTML(at.Icon)),
		})
	}
	ctx.JSON(http.StatusOK, out)
}
