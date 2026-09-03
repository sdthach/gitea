// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	"gitea.dev/models/perm/access"
	planning_model "gitea.dev/models/planning"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/json"
	"gitea.dev/modules/optional"
	"gitea.dev/modules/timeutil"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	issue_service "gitea.dev/services/issue"
	planning_service "gitea.dev/services/planning"
)

// The roadmap's writes. Every one goes through Gitea's own service layer as the calling
// user, so each leaves the record Gitea leaves for it — a milestone comment, a deadline
// comment, a new issue — and the fork keeps no second history of the same edits.
//
// A start date is the exception with no Gitea field behind it: Gitea stores no start, so a
// write records it in plan_issue_schedule instead, and nothing in the repository is touched.

const maxWriteBody = 16 << 10

// writeBody is every field the four writes take between them. One shape keeps the
// repository resolution and the permission check in one place.
type writeBody struct {
	Repo          string `json:"repo"`
	MilestoneID   int64  `json:"milestone_id"`
	Start         string `json:"start"`
	End           string `json:"end"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	ParentIssueID int64  `json:"parent_issue_id"`
	GroupBy       string `json:"group_by"`
	Group         string `json:"group"`
	TimeEstimate  string `json:"time_estimate"`
	TypeID        int64  `json:"type_id"`
}

var repoParam = []hubapi.Param{
	{Name: "repo", In: "body", Type: "string", Required: true, Description: "Repository as owner/name."},
}

var issueParam = []hubapi.Param{
	{Name: "issue_id", In: "path", Type: "integer", Required: true, Description: "The issue's global id, as the roadmap's bars publish it."},
}

func moveIssueMilestoneEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "moveIssueMilestone", Method: http.MethodPost, Path: "/issues/{issue_id}/milestone",
			Summary: "Move an issue between the chart's milestone rows",
			Description: "Assigns the issue's milestone through Gitea's own ChangeMilestoneAssign, which records " +
				"the change as a milestone comment on the issue. Send milestone_id 0 to take the issue off every row. " +
				"Authorized by Gitea's own write check on the Issues unit.",
			Tag: "roadmap", PathParams: issueParam,
			Body: append(append([]hubapi.Param{}, repoParam...),
				hubapi.Param{Name: "milestone_id", In: "body", Type: "integer", Description: "Target milestone; 0 removes the issue from its row."}),
			CLINames: []string{"issue-move-milestone"},
			Response: "Roadmap", ResponseIs: "object",
		},
		Handler: MoveIssueMilestone,
	}
}

func setIssueDatesEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "setIssueDates", Method: http.MethodPost, Path: "/issues/{issue_id}/dates",
			Summary: "Set a bar's start and end",
			Description: "The end is Issue.DeadlineUnix, written through Gitea's own update, which records a deadline " +
				"comment. The start is a recorded plan_issue_schedule row rather than a Gitea field — see PUT " +
				"/issues/{issue_id}/schedule for the endpoint dedicated to it. Send either field empty to leave it as it " +
				"stands. Authorized by Gitea's own write check on the Issues unit.",
			Tag: "roadmap", PathParams: issueParam,
			Body: append(append([]hubapi.Param{}, repoParam...),
				hubapi.Param{Name: "start", In: "body", Type: "string", Description: "Bar start as an RFC 3339 timestamp or a YYYY-MM-DD date."},
				hubapi.Param{Name: "end", In: "body", Type: "string", Description: "Bar end as an RFC 3339 timestamp or a YYYY-MM-DD date."}),
			CLINames: []string{"issue-set-dates"},
			Response: "Roadmap", ResponseIs: "object",
		},
		Handler: SetIssueDates,
	}
}

func moveIssueGroupEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "moveIssueGroup", Method: http.MethodPost, Path: "/issues/{issue_id}/group",
			Summary: "Move a bar between the chart's groups",
			Description: "The chart's vertical drag. A group IS the grouping value, so moving between groups edits the " +
				"field itself: the issue's assigned type, its recorded parent, or the assignee. It goes through the same PlanGroupMove " +
				"the board's group move goes through, so a vertical drag on the chart and a group move on the board are one " +
				"operation with one definition rather than two that can drift. Dragging vertically writes the grouping " +
				"field and dragging horizontally writes dates; the two are independent. " +
				"It is REFUSED when grouping is off, because there is then nothing to write, and the refusal says so. " +
				"Authorized by Gitea's own write check on the Issues unit.",
			Tag: "roadmap", PathParams: issueParam,
			Body: append(append([]hubapi.Param{}, repoParam...),
				hubapi.Param{
					Name: "group_by", In: "body", Type: "string", Required: true, Enum: planning_service.Groupings,
					Description: "The active grouping. A group move is refused when this is none, because there is nothing to write.",
				},
				hubapi.Param{
					Name: "group", In: "body", Type: "string",
					Description: "The group's key: the type name, the assignee login, or — under parent grouping — the root issue's id as a string. Empty moves the bar into the empty-value group, clearing the field.",
				}),
			CLINames: []string{"issue-move-group"},
			Response: "Roadmap", ResponseIs: "object",
		},
		Handler: MoveIssueGroup,
	}
}

func createMilestoneEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "createMilestone", Method: http.MethodPost, Path: "/milestones",
			Summary:     "Create a milestone row",
			Description: "Creates the milestone the chart draws as a row. Authorized by Gitea's own write check on the Issues unit.",
			Tag:         "roadmap",
			Body: append(append([]hubapi.Param{}, repoParam...),
				hubapi.Param{Name: "title", In: "body", Type: "string", Required: true, Description: "Milestone title."},
				hubapi.Param{Name: "description", In: "body", Type: "string", Description: "Milestone description."},
				hubapi.Param{Name: "end", In: "body", Type: "string", Description: "Milestone deadline as an RFC 3339 timestamp or a YYYY-MM-DD date."}),
			CLINames: []string{"milestone-create"},
			Response: "Roadmap", ResponseIs: "object",
		},
		Handler: CreateMilestone,
	}
}

func createIssueEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "createIssue", Method: http.MethodPost, Path: "/issues",
			Summary: "Create an issue on a row",
			Description: "Creates the issue and files it under the milestone row. type_id and parent_issue_id are " +
				"both optional and both validated before anything is created: the type must be visible from the " +
				"repository (type_not_visible), and a parent must exist in the same repository, be readable, carry a " +
				"type, and satisfy RankAllows against type_id (rank_mismatch); a parent given without type_id is " +
				"refused untyped_issue, naming the new issue. " +
				"Authorized by Gitea's own write check on the Issues unit.",
			Tag: "roadmap",
			Body: append(append([]hubapi.Param{}, repoParam...),
				hubapi.Param{Name: "title", In: "body", Type: "string", Required: true, Description: "Issue title."},
				hubapi.Param{Name: "description", In: "body", Type: "string", Description: "Issue body."},
				hubapi.Param{Name: "milestone_id", In: "body", Type: "integer", Description: "Milestone row to file it under."},
				hubapi.Param{Name: "type_id", In: "body", Type: "integer", Description: "A type visible from the repository, assigned to the new issue."},
				hubapi.Param{Name: "parent_issue_id", In: "body", Type: "integer", Description: "A parent in the same repository; needs type_id, and the parent's own type must outrank it."}),
			CLINames: []string{"issue-create"},
			Response: "Roadmap", ResponseIs: "object",
		},
		Handler: CreateIssue,
	}
}

// MoveIssueMilestone answers POST /issues/{issue_id}/milestone.
func MoveIssueMilestone(ctx *context.APIContext) {
	body, repo, issue, ok := issueTarget(ctx)
	if !ok {
		return
	}
	if body.MilestoneID != 0 && !milestoneBelongs(ctx, repo, body.MilestoneID) {
		return
	}
	if issue.MilestoneID != body.MilestoneID {
		oldMilestoneID := issue.MilestoneID
		issue.MilestoneID = body.MilestoneID
		if err := issue_service.ChangeMilestoneAssign(ctx, issue, ctx.Doer, oldMilestoneID); err != nil {
			ctx.APIErrorInternal(err)
			return
		}
	}
	renderRoadmapAfterWrite(ctx, repo, roadmapView{})
}

// SetIssueDates answers POST /issues/{issue_id}/dates.
func SetIssueDates(ctx *context.APIContext) {
	body, repo, issue, ok := issueTarget(ctx)
	if !ok {
		return
	}

	start, ok := parseDate(ctx, "start", body.Start)
	if !ok {
		return
	}
	end, ok := parseDate(ctx, "end", body.End)
	if !ok {
		return
	}
	if start == 0 && end == 0 {
		hubapi.APIError(ctx, http.StatusBadRequest, "no_dates",
			"neither start nor end was given, so there is nothing to set",
			"Send start, end, or both, as an RFC 3339 timestamp or a YYYY-MM-DD date.")
		return
	}

	if end != 0 {
		if err := issues_model.UpdateIssueDeadline(ctx, issue, timeutil.TimeStamp(end), ctx.Doer); err != nil {
			ctx.APIErrorInternal(err)
			return
		}
	}
	if start != 0 {
		if err := planning_service.SetIssueStart(ctx, issue, time.Unix(start, 0).UTC()); err != nil {
			hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
			return
		}
	}
	renderRoadmapAfterWrite(ctx, repo, roadmapView{})
}

// MoveIssueGroup answers POST /issues/{issue_id}/group. It delegates to the
// same PlanGroupMove the board's group move delegates to, so there is one definition of what a
// group move writes rather than one per view.
func MoveIssueGroup(ctx *context.APIContext) {
	body, repo, issue, ok := issueTarget(ctx)
	if !ok {
		return
	}
	grouping, ok := parseGrouping(ctx, body.GroupBy)
	if !ok {
		return
	}
	write, err := planning_service.PlanGroupMove(grouping, body.Group)
	if err != nil {
		// With grouping off there is no field to write, and the message says which write
		// does still work.
		hubapi.RenderHubError(ctx, http.StatusBadRequest, err)
		return
	}

	switch write.Kind {
	case planning_service.GroupWriteType:
		if !applyGroupType(ctx, issue, write) {
			return
		}
	case planning_service.GroupWriteParent:
		if !applyGroupParent(ctx, issue, write) {
			return
		}
	case planning_service.GroupWriteAssignee:
		if !applyGroupAssignee(ctx, issue, write) {
			return
		}
	}
	renderRoadmapAfterWrite(ctx, repo, roadmapView{grouping: grouping, zoom: planning_service.ZoomIssue})
}

// CreateMilestone answers POST /milestones.
func CreateMilestone(ctx *context.APIContext) {
	body, repo, ok := writeTarget(ctx)
	if !ok {
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		hubapi.APIError(ctx, http.StatusBadRequest, "missing_title",
			"a milestone row needs a title", "Send title with the request.")
		return
	}
	deadline, ok := parseDate(ctx, "end", body.End)
	if !ok {
		return
	}
	milestone := &issues_model.Milestone{
		RepoID: repo.ID, Name: title, Content: body.Description,
		DeadlineUnix: timeutil.TimeStamp(deadline),
	}
	if err := issues_model.NewMilestone(ctx, milestone); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	renderRoadmapAfterWrite(ctx, repo, roadmapView{})
}

// CreateIssue answers POST /issues.
func CreateIssue(ctx *context.APIContext) {
	body, repo, ok := writeTarget(ctx)
	if !ok {
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		hubapi.APIError(ctx, http.StatusBadRequest, "missing_title",
			"an issue needs a title", "Send title with the request.")
		return
	}
	if body.MilestoneID != 0 && !milestoneBelongs(ctx, repo, body.MilestoneID) {
		return
	}
	if !validateNewIssueHierarchy(ctx, repo, body) {
		return
	}

	issue := &issues_model.Issue{
		RepoID: repo.ID, Repo: repo, Title: title, Content: body.Description,
		PosterID: ctx.Doer.ID, Poster: ctx.Doer, MilestoneID: body.MilestoneID,
	}
	if err := issue_service.NewIssue(ctx, repo, issue, nil, nil, nil, nil); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if body.TypeID != 0 {
		if err := planning_service.SetIssueType(ctx, issue, body.TypeID); err != nil {
			hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
			return
		}
	}
	if body.ParentIssueID != 0 {
		parent, err := issues_model.GetIssueByID(ctx, body.ParentIssueID)
		if err != nil {
			ctx.APIErrorInternal(err)
			return
		}
		if err := planning_service.SetIssueParent(ctx, issue, parent); err != nil {
			hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
			return
		}
	}
	renderRoadmapAfterWrite(ctx, repo, roadmapView{})
}

// visibleType resolves typeID to the type visible from repo, the same lookup SetIssueType
// makes after an issue already exists — done here first so a refused create leaves no row.
func visibleType(ctx *context.APIContext, repo *repo_model.Repository, typeID int64) (*planning_service.VisibleType, bool) {
	types, err := planning_service.TypesFor(ctx, repo)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}
	for i := range types {
		if types[i].ID == typeID {
			return &types[i], true
		}
	}
	hubapi.APIError(ctx, http.StatusUnprocessableEntity, "type_not_visible",
		"that type is not visible from this repository",
		"Use one of the ids GET "+BasePath+"/issue-types?repo_id=<repo_id> returns for this repository.")
	return nil, false
}

// validateNewIssueHierarchy validates type_id and parent_issue_id before anything is created:
// the type must be visible from repo, the parent must exist in repo, and the two ranks must
// satisfy RankAllows — the same rule SetIssueType and SetIssueParent enforce once an issue
// already exists, checked here first so a refused create leaves no row behind.
func validateNewIssueHierarchy(ctx *context.APIContext, repo *repo_model.Repository, body *writeBody) bool {
	var newType *planning_service.VisibleType
	if body.TypeID != 0 {
		var ok bool
		newType, ok = visibleType(ctx, repo, body.TypeID)
		if !ok {
			return false
		}
	}
	if body.ParentIssueID == 0 {
		return true
	}
	parent, err := issues_model.GetIssueByID(ctx, body.ParentIssueID)
	if err != nil || parent.RepoID != repo.ID {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "parent_not_found",
			"no issue with that id belongs to "+repo.FullName(),
			"Read the chart's rows from "+BasePath+"/roadmap and use one of their issue_id values.")
		return false
	}

	parentAssignment, err := planning_model.AssignmentsFor(ctx, []int64{parent.ID})
	if err != nil {
		ctx.APIErrorInternal(err)
		return false
	}
	parentTypeID, parentTyped := parentAssignment[parent.ID]
	var parentType *planning_model.IssueType
	if parentTyped {
		types, err := planning_model.GetIssueTypesByIDs(ctx, []int64{parentTypeID})
		if err != nil {
			ctx.APIErrorInternal(err)
			return false
		}
		parentType, parentTyped = types[parentTypeID]
	}
	if !parentTyped {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "untyped_issue",
			"the parent carries no type, and hierarchy needs one on both sides to rank them",
			"Assign a type to the parent first.")
		return false
	}
	if newType == nil {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "untyped_issue",
			"the new issue carries no type, and hierarchy needs one on both sides to rank them",
			"Send type_id with the request.")
		return false
	}
	if !planning_service.RankAllows(parentType.Rank, newType.Rank) {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "rank_mismatch",
			fmt.Sprintf("%s (rank %d) does not outrank %s (rank %d)", parentType.Name, parentType.Rank, newType.Name, newType.Rank),
			"Choose a parent whose type outranks the new issue's, or change one of the two types.")
		return false
	}
	return true
}

// writeTarget reads the body, resolves the repository and applies Gitea's own write
// check on the Issues unit. Visibility is answered before permission, so a caller who cannot
// see the repository is not told it exists.
func writeTarget(ctx *context.APIContext) (*writeBody, *repo_model.Repository, bool) {
	body, ok := readWriteBody(ctx)
	if !ok {
		return nil, nil, false
	}
	owner, name, found := strings.Cut(strings.TrimSpace(body.Repo), "/")
	if !found || owner == "" || name == "" {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_repo",
			fmt.Sprintf("repo must be owner/name, got %q", body.Repo),
			"Send repo as owner/name, for example \"acme/widgets\".")
		return nil, nil, false
	}
	repo, err := repo_model.GetRepositoryByOwnerAndName(ctx, owner, name)
	if err != nil {
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository "+owner+"/"+name+" is visible to you",
			"Check the owner and repository name against "+BasePath+"/repos.")
		return nil, nil, false
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, nil, false
	}
	if !perm.CanRead(unit.TypeIssues) {
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository "+owner+"/"+name+" is visible to you",
			"Check the owner and repository name against "+BasePath+"/repos.")
		return nil, nil, false
	}
	if !perm.CanWrite(unit.TypeIssues) {
		hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
			"your account has no write access to the Issues unit of "+repo.FullName(),
			"Ask a repository administrator for write permission on Issues.")
		return nil, nil, false
	}
	return body, repo, true
}

// issueTarget adds the issue the path names to what writeTarget resolves.
func issueTarget(ctx *context.APIContext) (*writeBody, *repo_model.Repository, *issues_model.Issue, bool) {
	body, repo, ok := writeTarget(ctx)
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
	if err := issue.LoadAttributes(ctx); err != nil {
		ctx.APIErrorInternal(err)
		return nil, nil, nil, false
	}
	return body, repo, issue, true
}

func milestoneBelongs(ctx *context.APIContext, repo *repo_model.Repository, milestoneID int64) bool {
	milestone, err := issues_model.GetMilestoneByRepoID(ctx, repo.ID, milestoneID)
	if err != nil || milestone == nil {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "milestone_not_found",
			"no milestone with that id belongs to "+repo.FullName(),
			"Read the chart's rows from "+BasePath+"/roadmap and use one of their milestone_id values.")
		return false
	}
	return true
}

// dateFormats are what a date may be sent as: a full timestamp, or the day alone,
// which is what a chart's own controls produce.
var dateFormats = []string{time.RFC3339, "2006-01-02"}

// parseDate returns 0 for an empty value, which every caller reads as "leave it".
func parseDate(ctx *context.APIContext, field, raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	for _, format := range dateFormats {
		if at, err := time.Parse(format, raw); err == nil {
			return at.UTC().Unix(), true
		}
	}
	hubapi.APIError(ctx, http.StatusBadRequest, "bad_date",
		fmt.Sprintf("%s is not a date this endpoint reads: %q", field, raw),
		"Send an RFC 3339 timestamp, for example 2026-03-01T00:00:00Z, or a YYYY-MM-DD date.")
	return 0, false
}

func readWriteBody(ctx *context.APIContext) (*writeBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(ctx.Req.Body, maxWriteBody+1))
	if err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "unreadable_body",
			"the request body could not be read", "Retry the request.")
		return nil, false
	}
	if len(raw) > maxWriteBody {
		hubapi.APIError(ctx, http.StatusBadRequest, "body_too_large",
			"the request body is larger than 16KiB",
			"A roadmap write is small; check whether the request is sending the right content.")
		return nil, false
	}
	body := new(writeBody)
	if err := json.Unmarshal(raw, body); err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"repo": "acme/widgets", "milestone_id": 3}.`)
		return nil, false
	}
	return body, true
}

// renderRoadmapAfterWrite answers with the chart the write produced, over the same
// projection GET serves, so a client never has to guess what its write did.
func renderRoadmapAfterWrite(ctx *context.APIContext, repo *repo_model.Repository, view roadmapView) {
	const limit = 200
	renderRoadmap(ctx, repo, &issues_model.IssuesOptions{
		RepoIDs:   []int64{repo.ID},
		IsPull:    optional.Some(false),
		Paginator: &db.ListOptions{Page: 1, PageSize: limit},
		SortType:  "oldest",
	}, limit, true, view) // every caller here has already passed the write check
}
