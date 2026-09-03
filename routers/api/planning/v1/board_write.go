// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	issues_model "gitea.dev/models/issues"
	"gitea.dev/models/perm/access"
	project_model "gitea.dev/models/project"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/json"
	"gitea.dev/modules/optional"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	issue_service "gitea.dev/services/issue"
	planning_service "gitea.dev/services/planning"
	project_service "gitea.dev/services/projects"
)

// maxOrderIssueIDs bounds one reorder call: a whole-column drag on a real board, not a bulk
// import.
const maxOrderIssueIDs = 500

// maxCardTitle matches Gitea's own issue title limit.
const maxCardTitle = 255

// boardWritePerm resolves and authorizes the repository a board write names: visibility first,
// so a caller who cannot see the repository is not told how its Projects unit is configured;
// then availability, so an absent unit is never reported as a permission problem; then read
// access to Projects; then write access to needed. Both board.go's card writes and this file's
// column and card writes share it.
func boardWritePerm(ctx *context.APIContext, repoRef string, needed unit.Type) (*repo_model.Repository, access.Permission, bool) {
	var zero access.Permission
	owner, name, found := strings.Cut(strings.TrimSpace(repoRef), "/")
	if !found || owner == "" || name == "" {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_repo",
			"repo must be owner/name, got "+repoRef,
			"Send repo as owner/name, for example \"acme/widgets\".")
		return nil, zero, false
	}
	repo, err := repo_model.GetRepositoryByOwnerAndName(ctx, owner, name)
	if err != nil {
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository "+owner+"/"+name+" is visible to you",
			"Check the owner and repository name against "+BasePath+"/repos.")
		return nil, zero, false
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, zero, false
	}
	if !boardReadable(ctx, perm) {
		return nil, zero, false
	}
	if !boardAvailable(ctx, repo) {
		return nil, zero, false
	}
	if !boardProjectsReadable(ctx, repo, perm) {
		return nil, zero, false
	}
	if !perm.CanWrite(needed) {
		hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
			"your account has no write access to the "+needed.LogString()+" unit of "+repo.FullName(),
			"Ask a repository administrator for write permission on that unit.")
		return nil, zero, false
	}
	return repo, perm, true
}

// readBoundedBody reads a board write's body under maxBoardMoveBody, the same limit board.go's
// two writes use.
func readBoundedBody(ctx *context.APIContext) ([]byte, bool) {
	raw, err := io.ReadAll(io.LimitReader(ctx.Req.Body, maxBoardMoveBody+1))
	if err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "unreadable_body",
			"the request body could not be read", "Retry the request; the body was cut short.")
		return nil, false
	}
	if len(raw) > maxBoardMoveBody {
		hubapi.APIError(ctx, http.StatusBadRequest, "body_too_large",
			"the request body is larger than 16KiB",
			"A board write carries ids and short text; send only those fields.")
		return nil, false
	}
	return raw, true
}

// issueIDsField accepts issue_ids as a JSON array (of numbers or strings — what the CLI's own
// comma-separated flag marshals to) or a bare comma-separated string, so the generated CLI and
// a direct API caller both work. A token is not validated here — an unnumeric or duplicate one
// answers bad_issue_ids from parseIssueIDs, not malformed_body from this.
type issueIDsField []string

func (f *issueIDsField) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var nums []int64
		if err := json.Unmarshal(data, &nums); err == nil {
			out := make([]string, len(nums))
			for i, n := range nums {
				out[i] = strconv.FormatInt(n, 10)
			}
			*f = out
			return nil
		}
		var strs []string
		if err := json.Unmarshal(data, &strs); err != nil {
			return err
		}
		*f = strs
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		*f = nil
		return nil
	}
	*f = strings.Split(s, ",")
	return nil
}

// parseIssueIDsOrReason validates issue_ids' raw tokens with no dependency on a request: not
// empty, not over the limit, every token a distinct integer. Split from parseIssueIDs so the
// count boundary — 500 accepted, 501 refused — is a plain unit test with no live APIContext.
func parseIssueIDsOrReason(raw []string) (ids []int64, reason string, ok bool) {
	if len(raw) == 0 {
		return nil, "issue_ids must not be empty", false
	}
	if len(raw) > maxOrderIssueIDs {
		return nil, fmt.Sprintf("issue_ids carries %d ids, over the limit of %d", len(raw), maxOrderIssueIDs), false
	}
	seen := make(map[int64]bool, len(raw))
	ids = make([]int64, 0, len(raw))
	for _, token := range raw {
		token = strings.TrimSpace(token)
		id, err := strconv.ParseInt(token, 10, 64)
		if err != nil {
			return nil, "issue_ids contains a non-numeric id: " + token, false
		}
		if seen[id] {
			return nil, fmt.Sprintf("issue_ids contains duplicate id %d", id), false
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, "", true
}

// parseIssueIDs validates issue_ids' raw tokens, rendering the refusal named by
// parseIssueIDsOrReason.
func parseIssueIDs(ctx *context.APIContext, raw []string) ([]int64, bool) {
	ids, reason, ok := parseIssueIDsOrReason(raw)
	if !ok {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "bad_issue_ids", reason,
			fmt.Sprintf("Send a comma-separated list or a JSON array of up to %d distinct issue ids.", maxOrderIssueIDs))
		return nil, false
	}
	return ids, true
}

// missingColumnIssueIDs lists, in existing's own order, every id already in the column that
// given does not name: a reorder must carry the whole column, not a subset.
func missingColumnIssueIDs(existing, given []int64) []int64 {
	givenSet := make(map[int64]bool, len(given))
	for _, id := range given {
		givenSet[id] = true
	}
	missing := make([]int64, 0)
	for _, id := range existing {
		if !givenSet[id] {
			missing = append(missing, id)
		}
	}
	return missing
}

// orderColumnBody is POST /board/columns/{column_id}/order's own request shape.
type orderColumnBody struct {
	Repo      string        `json:"repo"`
	ProjectID int64         `json:"project_id"`
	IssueIDs  issueIDsField `json:"issue_ids"`
	GroupBy   string        `json:"group_by"`
}

func readOrderColumnBody(ctx *context.APIContext) (*orderColumnBody, bool) {
	raw, ok := readBoundedBody(ctx)
	if !ok {
		return nil, false
	}
	body := new(orderColumnBody)
	if err := json.Unmarshal(raw, body); err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"repo": "owner/name", "project_id": 5, "issue_ids": "12,7,3"} to reorder a column.`)
		return nil, false
	}
	return body, true
}

// orderColumnTarget resolves and authorizes a column reorder: the repository, the board, the
// target column and the Projects write permission — no Issues permission is needed, since a
// reorder writes only sorting.
func orderColumnTarget(ctx *context.APIContext) (*orderColumnBody, *repo_model.Repository, access.Permission, *project_model.Project, *project_model.Column, bool) {
	var zero access.Permission
	body, ok := readOrderColumnBody(ctx)
	if !ok {
		return nil, nil, zero, nil, nil, false
	}
	repo, perm, ok := boardWritePerm(ctx, body.Repo, unit.TypeProjects)
	if !ok {
		return nil, nil, zero, nil, nil, false
	}
	project, ok := boardProject(ctx, repo, body.ProjectID)
	if !ok {
		return nil, nil, zero, nil, nil, false
	}
	column, err := project_model.GetColumnByIDAndProjectID(ctx, ctx.PathParamInt64("column_id"), project.ID)
	if err != nil {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "column_not_in_project",
			"no column with that id belongs to this board",
			"Read the board's columns from "+BasePath+"/board and use one of their column_id values.")
		return nil, nil, zero, nil, nil, false
	}
	// Validated here, before any write: a bad grouping must leave every sorting untouched.
	if _, ok := parseGrouping(ctx, body.GroupBy); !ok {
		return nil, nil, zero, nil, nil, false
	}
	return body, repo, perm, project, column, true
}

var orderColumnPathParams = []hubapi.Param{
	{Name: "column_id", In: "path", Type: "integer", Required: true, Description: "The column being reordered."},
}

var orderColumnParams = []hubapi.Param{
	{Name: "repo", In: "body", Type: "string", Required: true, Description: "Repository as owner/name."},
	{Name: "project_id", In: "body", Type: "integer", Required: true, Description: "The board the column belongs to."},
	{
		Name: "issue_ids", In: "body", Type: "array", Required: true,
		Description: fmt.Sprintf("Every card in the column, in order, up to %d: a comma-separated list or a JSON array of issue ids, every id already a card of this repository and this project. A card in the column missing from this list answers incomplete_column and writes nothing — send the whole column, never a subset.", maxOrderIssueIDs),
	},
	{Name: "group_by", In: "body", Type: "string", Enum: planning_service.Groupings, Description: "The grouping to answer the board at. Omit for none."},
}

func orderBoardColumnEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "orderBoardColumn", Method: http.MethodPost, Path: "/board/columns/{column_id}/order",
			Summary: "Reorder every card in one column",
			Description: "GitHub Projects' own board reorders a whole column in one drag; this is the same write, " +
				"sorting issue_ids 0..n-1 in the order given through Gitea's own MoveIssuesOnProjectColumn. " +
				"issue_ids must send every card in the column, in order — one already in the column but missing from " +
				"the list answers incomplete_column, naming the ids left out. Every id must already be a card of this " +
				"repository and this project — one that is not answers issue_not_in_repo or issue_not_in_project. " +
				"Nothing is written on any refusal. Authorized by Gitea's own write check on the Projects unit.",
			Tag: "board", PathParams: orderColumnPathParams, Body: orderColumnParams,
			CLINames: []string{"board-order-column"},
			Response: "Board", ResponseIs: "object",
		},
		Handler: OrderBoardColumn,
	}
}

// OrderBoardColumn answers POST /board/columns/{column_id}/order: every card in the column,
// resorted in the order issue_ids gives, the one call GitHub Projects' own board makes for a
// whole-column drag.
func OrderBoardColumn(ctx *context.APIContext) {
	body, repo, perm, project, column, ok := orderColumnTarget(ctx)
	if !ok {
		return
	}
	ids, ok := parseIssueIDs(ctx, body.IssueIDs)
	if !ok {
		return
	}

	existing, err := project_model.GetColumnIssueIDs(ctx, column)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if missing := missingColumnIssueIDs(existing, ids); len(missing) > 0 {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "incomplete_column",
			fmt.Sprintf("issue_ids is missing %v, already in this column", missing),
			"Send every card in the column, in order; resend the whole column rather than a subset.")
		return
	}

	issues, err := issues_model.GetIssuesByIDs(ctx, ids)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	byID := make(map[int64]*issues_model.Issue, len(issues))
	for _, issue := range issues {
		byID[issue.ID] = issue
	}
	for _, id := range ids {
		if issue, found := byID[id]; !found || issue.RepoID != repo.ID {
			hubapi.APIError(ctx, http.StatusUnprocessableEntity, "issue_not_in_repo",
				fmt.Sprintf("issue %d does not belong to %s", id, repo.FullName()),
				"Only ids that are already cards of this repository's board may be reordered.")
			return
		}
	}

	sorted := make(map[int64]int64, len(ids))
	for i, id := range ids {
		sorted[int64(i)] = id
	}
	if err := project_service.MoveIssuesOnProjectColumn(ctx, ctx.Doer, column, sorted); err != nil {
		if errors.Is(err, project_service.ErrIssueNotInProject) {
			hubapi.APIError(ctx, http.StatusUnprocessableEntity, "issue_not_in_project",
				"all issues have to be added to a project first",
				"Add the issue to this project before ordering it, or drop it from issue_ids.")
			return
		}
		ctx.APIErrorInternal(err)
		return
	}
	renderBoardAfterWrite(ctx, repo, perm, project, body.GroupBy)
}

// addCardBody is POST /board/cards' own request shape: it creates the card rather than
// addressing one, so it carries a title and no issue_id path parameter.
type addCardBody struct {
	Repo      string `json:"repo"`
	ProjectID int64  `json:"project_id"`
	ColumnID  int64  `json:"column_id"`
	Title     string `json:"title"`
	GroupBy   string `json:"group_by"`
	Group     string `json:"group"`
	TypeID    int64  `json:"type_id"`
}

func readAddCardBody(ctx *context.APIContext) (*addCardBody, bool) {
	raw, ok := readBoundedBody(ctx)
	if !ok {
		return nil, false
	}
	body := new(addCardBody)
	if err := json.Unmarshal(raw, body); err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"repo": "owner/name", "project_id": 5, "column_id": 1, "title": "Wire it"} to add a card.`)
		return nil, false
	}
	return body, true
}

// addCardTarget resolves and authorizes a card creation: it needs both units GitHub Projects'
// own "add item" needs — Issues, to create the issue, and Projects, to place it on the board.
func addCardTarget(ctx *context.APIContext) (*addCardBody, *repo_model.Repository, access.Permission, *project_model.Project, bool) {
	var zero access.Permission
	body, ok := readAddCardBody(ctx)
	if !ok {
		return nil, nil, zero, nil, false
	}
	repo, perm, ok := boardWritePerm(ctx, body.Repo, unit.TypeIssues)
	if !ok {
		return nil, nil, zero, nil, false
	}
	if !perm.CanWrite(unit.TypeProjects) {
		hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
			"your account has no write access to the "+unit.TypeProjects.LogString()+" unit of "+repo.FullName(),
			"Ask a repository administrator for write permission on that unit.")
		return nil, nil, zero, nil, false
	}
	project, ok := boardProject(ctx, repo, body.ProjectID)
	if !ok {
		return nil, nil, zero, nil, false
	}
	return body, repo, perm, project, true
}

var addCardParams = []hubapi.Param{
	{Name: "repo", In: "body", Type: "string", Required: true, Description: "Repository as owner/name."},
	{Name: "project_id", In: "body", Type: "integer", Required: true, Description: "The board to add the card to."},
	{Name: "column_id", In: "body", Type: "integer", Required: true, Description: "The column the new card lands in."},
	{Name: "title", In: "body", Type: "string", Required: true, Description: fmt.Sprintf("The new issue's title, 1-%d characters.", maxCardTitle)},
	{
		Name: "group_by", In: "body", Type: "string", Enum: planning_service.Groupings,
		Description: "The grouping group is read under, and the board is answered at. Omit for no group.",
	},
	{
		Name: "group", In: "body", Type: "string",
		Description: "The group's key, resolved exactly as a group move resolves one: a type name, an assignee login, or a root issue id under parent grouping. A non-empty parent group needs type_id, since a new card otherwise starts with no type for hierarchy to rank.",
	},
	{
		Name: "type_id", In: "body", Type: "integer",
		Description: "The new card's type, assigned right after it is created. Must be visible from repo, or the request answers type_not_visible. Required when group_by is parent and group is non-empty.",
	},
}

func addBoardCardEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "addBoardCard", Method: http.MethodPost, Path: "/board/cards",
			Summary: "Create an issue directly onto the board",
			Description: "GitHub Projects' own board adds an item straight into a column and a group; this is the same " +
				"write over Gitea's own project card. The group is resolved before the issue is created — the same " +
				"codes a group move answers, for the dimensions a brand new issue can carry (type, assignee, parent) " +
				"— so a refused group leaves nothing behind. A non-empty parent group needs type_id, checked visible " +
				"before creation, because a new card otherwise has no type yet for hierarchy to rank; type_id itself " +
				"is assigned right after the issue is created, before its group is written. Creating the issue, " +
				"assigning its type, placing it in its column and writing its group are separate calls: if any but " +
				"the first fails, the issue already exists incomplete, and the response is a 500 that says so, " +
				"because pre-creation validation has already excluded every refusal that means something to the " +
				"caller. Authorized by Gitea's own write check on both the Issues and Projects units.",
			Tag: "board", Body: addCardParams,
			CLINames: []string{"board-add-card"},
			Response: "Board", ResponseIs: "object",
		},
		Handler: AddBoardCard,
	}
}

// AddBoardCard answers POST /board/cards: the second and last of the board's writes GitHub
// Projects' own board makes that this fork does not already have a Gitea endpoint for.
func AddBoardCard(ctx *context.APIContext) {
	body, repo, perm, project, ok := addCardTarget(ctx)
	if !ok {
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" || len(title) > maxCardTitle {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "bad_title",
			fmt.Sprintf("title must be 1-%d characters", maxCardTitle),
			"Send a shorter, non-empty title.")
		return
	}
	column, err := project_model.GetColumnByIDAndProjectID(ctx, body.ColumnID, project.ID)
	if err != nil {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "column_not_in_project",
			"no column with that id belongs to this board",
			"Read the board's columns from "+BasePath+"/board and use one of their column_id values.")
		return
	}
	grouping, ok := parseGrouping(ctx, body.GroupBy)
	if !ok {
		return
	}
	var newType planning_service.VisibleType
	if body.TypeID != 0 {
		newType, ok = resolveCardType(ctx, repo, body.TypeID)
		if !ok {
			return
		}
	}
	write, ok := resolveAddCardGroup(ctx, repo, grouping, body.Group, body.TypeID)
	if !ok {
		return
	}

	issue := &issues_model.Issue{
		RepoID: repo.ID, Repo: repo, Title: title, PosterID: ctx.Doer.ID, Poster: ctx.Doer,
	}
	if err := issue_service.NewIssue(ctx, repo, issue, nil, nil, nil, []int64{project.ID}); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if body.TypeID != 0 {
		// Assigned before the group write, so a parent group can rank the new card.
		if err := planning_service.SetIssueType(ctx, issue, newType.ID); err != nil {
			hubapi.APIError(ctx, http.StatusInternalServerError, "card_incomplete",
				"the card was created but its type could not be assigned: "+err.Error(),
				"Assign it with PUT "+BasePath+"/issues/{issue_id}/type.")
			return
		}
	}
	if err := project_service.MoveIssueToColumn(ctx, ctx.Doer, issue, column, optional.None[int64]()); err != nil {
		hubapi.APIError(ctx, http.StatusInternalServerError, "card_incomplete",
			"the card was created but could not be placed in its column: "+err.Error(),
			"Move it with POST "+BasePath+"/board/cards/{issue_id}/column.")
		return
	}
	if err := addCardGroup(ctx, issue, write); err != nil {
		hubapi.APIError(ctx, http.StatusInternalServerError, "card_incomplete",
			"the card was created and placed but its group could not be written: "+err.Error(),
			"Move it with POST "+BasePath+"/board/cards/{issue_id}/group.")
		return
	}
	renderBoardAfterWrite(ctx, repo, perm, project, body.GroupBy)
}

// resolveCardType checks type_id names a type visible from repo, before the card that would
// carry it exists.
func resolveCardType(ctx *context.APIContext, repo *repo_model.Repository, typeID int64) (planning_service.VisibleType, bool) {
	types, err := planning_service.TypesFor(ctx, repo)
	if err != nil {
		ctx.APIErrorInternal(err)
		return planning_service.VisibleType{}, false
	}
	for _, t := range types {
		if t.ID == typeID {
			return t, true
		}
	}
	hubapi.APIError(ctx, http.StatusUnprocessableEntity, "type_not_visible",
		fmt.Sprintf("no type with id %d is visible from %s", typeID, repo.FullName()),
		"Create the type first, or send one of the ids GET "+BasePath+"/issue-types?repo_id="+
			strconv.FormatInt(repo.ID, 10)+" returns.")
	return planning_service.VisibleType{}, false
}

// resolveAddCardGroup validates a card-creation group before the card exists, answering the
// same codes a group move answers: a type by name, an assignee by login, a parent by whether
// the new card will carry a type (type_id) for hierarchy to rank it against.
func resolveAddCardGroup(ctx *context.APIContext, repo *repo_model.Repository, grouping planning_service.Grouping, groupKey string, typeID int64) (planning_service.GroupWrite, bool) {
	var zero planning_service.GroupWrite
	if grouping == planning_service.GroupNone && strings.TrimSpace(groupKey) == "" {
		return zero, true
	}
	// grouping == GroupNone with a non-empty group falls through to PlanGroupMove, which
	// refuses it exactly as moveBoardCardGroup's own PlanGroupMove call does: the board is
	// not grouped, so there is nothing to write.
	write, err := planning_service.PlanGroupMove(grouping, groupKey)
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusBadRequest, err)
		return zero, false
	}
	switch write.Kind {
	case planning_service.GroupWriteType:
		if !resolveTypeGroup(ctx, repo, write.TypeName) {
			return zero, false
		}
	case planning_service.GroupWriteParent:
		if !validateNewIssueHierarchy(ctx, repo, typeID, write.ParentIssueID) {
			return zero, false
		}
	case planning_service.GroupWriteAssignee:
		if !resolveAssigneeGroup(ctx, repo, write.Assignee) {
			return zero, false
		}
	}
	return write, true
}

func resolveTypeGroup(ctx *context.APIContext, repo *repo_model.Repository, name string) bool {
	if name == "" {
		return true
	}
	types, err := planning_service.TypesFor(ctx, repo)
	if err != nil {
		ctx.APIErrorInternal(err)
		return false
	}
	if _, ok := planning_service.FindVisibleType(types, name); ok {
		return true
	}
	hubapi.APIError(ctx, http.StatusUnprocessableEntity, "type_not_visible",
		"no type named "+name+" is visible from "+repo.FullName(),
		"Create the type first, or move to one of the names GET "+BasePath+"/issue-types?repo_id="+
			strconv.FormatInt(repo.ID, 10)+" returns.")
	return false
}

func resolveAssigneeGroup(ctx *context.APIContext, repo *repo_model.Repository, login string) bool {
	if login == "" {
		return true
	}
	assignee, err := findAssignee(ctx, &issues_model.Issue{Repo: repo}, login)
	if err != nil {
		ctx.APIErrorInternal(err)
		return false
	}
	if assignee != 0 {
		return true
	}
	hubapi.APIError(ctx, http.StatusUnprocessableEntity, "assignee_not_found",
		"no user named "+login+" can be assigned issues in this repository",
		"Move the card to a group whose login is an assignable user, or add the user to the repository first.")
	return false
}

// addCardGroup performs the group write once the card exists. Any failure here answers 500:
// for type and assignee groups resolveAddCardGroup already excluded every refusal that means
// anything to the caller, so reaching one is a race between validation and write; a parent
// group's SetIssueParent call can still refuse on rank or depth, which resolveParentPreCreate
// does not check ahead of creation.
func addCardGroup(ctx *context.APIContext, issue *issues_model.Issue, write planning_service.GroupWrite) error {
	if write.Kind == "" {
		return nil
	}
	if err := issue.LoadRepo(ctx); err != nil {
		return err
	}
	switch write.Kind {
	case planning_service.GroupWriteType:
		if write.TypeName == "" {
			return nil // a new card starts with no type
		}
		types, err := planning_service.TypesFor(ctx, issue.Repo)
		if err != nil {
			return err
		}
		t, ok := planning_service.FindVisibleType(types, write.TypeName)
		if !ok {
			return fmt.Errorf("type %q is no longer visible from %s", write.TypeName, issue.Repo.FullName())
		}
		return planning_service.SetIssueType(ctx, issue, t.ID)
	case planning_service.GroupWriteParent:
		if write.ParentIssueID == 0 {
			return planning_service.RemoveIssueParent(ctx, issue.ID)
		}
		parent, err := issues_model.GetIssueByID(ctx, write.ParentIssueID)
		if err != nil {
			return err
		}
		return planning_service.SetIssueParent(ctx, issue, parent) // resolveParentPreCreate already confirmed the parent and that the card carries a type
	case planning_service.GroupWriteAssignee:
		if write.Assignee == "" {
			return nil // a new card starts with no assignees
		}
		assignee, err := findAssignee(ctx, issue, write.Assignee)
		if err != nil {
			return err
		}
		if assignee == 0 {
			return fmt.Errorf("assignee %q is no longer assignable in %s", write.Assignee, issue.Repo.FullName())
		}
		_, _, err = issue_service.ToggleAssigneeWithNotify(ctx, issue, ctx.Doer, assignee)
		return err
	}
	return nil
}
