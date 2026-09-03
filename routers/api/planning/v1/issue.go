// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	issues_model "gitea.dev/models/issues"
	"gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/modules/util"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	issue_service "gitea.dev/services/issue"
	planning_service "gitea.dev/services/planning"
)

// ScheduleFacet is an issue's start, with the source a client would otherwise have to
// re-derive from the roadmap's own rule.
type ScheduleFacet struct {
	StartUnix   int64                        `json:"start_unix"`
	StartSource planning_service.StartSource `json:"start_source"`
}

// MilestoneFacet is the milestone an issue is filed under, with its own schedule.
type MilestoneFacet struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	StartUnix int64  `json:"start_unix"`
	DueUnix   int64  `json:"due_unix"`
}

// IssueFacets is one issue reduced to the fields the roadmap's side panel edits.
type IssueFacets struct {
	IssueID        int64           `json:"issue_id"`
	Number         int64           `json:"number"`
	RepoID         int64           `json:"repo_id"`
	CanWrite       bool            `json:"can_write"`
	Schedule       ScheduleFacet   `json:"schedule"`
	Milestone      *MilestoneFacet `json:"milestone"`
	TimeEstimate   int64           `json:"time_estimate"`
	TrackedSeconds int64           `json:"tracked_seconds"`
}

// MilestoneSchedule is a milestone reduced to its own schedule, the response a milestone
// schedule write replies with.
type MilestoneSchedule struct {
	MilestoneID int64  `json:"milestone_id"`
	Title       string `json:"title"`
	StartUnix   int64  `json:"start_unix"`
	DueUnix     int64  `json:"due_unix"`
}

var milestoneParam = []hubapi.Param{
	{Name: "milestone_id", In: "path", Type: "integer", Required: true, Description: "The milestone's id."},
}

func issueEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "issue", Method: http.MethodGet, Path: "/issues/{issue_id}",
			Summary: "The fields the roadmap's side panel edits: schedule, milestone, estimate and tracked time",
			Description: "Scoped by Gitea's own permission check on the Issues or Pull Requests unit, whichever the " +
				"issue belongs to; a caller who cannot read it gets 404 rather than 403, so the refusal never confirms " +
				"the issue exists. can_write says whether the write endpoints below will accept an edit from this caller.",
			Tag: "issues", PathParams: issueParam,
			CLINames: []string{"issue"},
			Response: "IssueFacets", ResponseIs: "object",
		},
		Handler: GetIssueFacets,
	}
}

func setIssueScheduleEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "setIssueSchedule", Method: http.MethodPut, Path: "/issues/{issue_id}/schedule",
			Summary: "Set an issue's start date",
			Description: "Records the start in plan_issue_schedule; refused for a pull request, a start at or before " +
				"the Unix epoch, or a start after the issue's own deadline when one is set. Authorized by Gitea's own " +
				"write check on the Issues unit.",
			Tag: "issues", PathParams: issueParam,
			Body: append(append([]hubapi.Param{}, repoParam...),
				hubapi.Param{Name: "start", In: "body", Type: "string", Required: true, Description: "Start as an RFC 3339 timestamp or a YYYY-MM-DD date."}),
			CLINames: []string{"issue-set-start"},
			Response: "IssueFacets", ResponseIs: "object",
		},
		Handler: SetIssueSchedule,
	}
}

func clearIssueScheduleEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "clearIssueSchedule", Method: http.MethodDelete, Path: "/issues/{issue_id}/schedule",
			Summary:     "Clear an issue's start date",
			Description: "Same authorization as setting it.",
			Tag:         "issues", PathParams: issueParam,
			Body:     append([]hubapi.Param{}, repoParam...),
			CLINames: []string{"issue-clear-start"},
			Response: "IssueFacets", ResponseIs: "object",
		},
		Handler: ClearIssueSchedule,
	}
}

func setMilestoneScheduleEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "setMilestoneSchedule", Method: http.MethodPut, Path: "/milestones/{milestone_id}/schedule",
			Summary: "Set a milestone's start date",
			Description: "Records the start in plan_milestone_schedule; refused for a milestone that does not belong " +
				"to repo, a start at or before the Unix epoch, or a start after the milestone's own due date when one " +
				"is set. Authorized by Gitea's own write check on the Issues unit.",
			Tag: "issues", PathParams: milestoneParam,
			Body: append(append([]hubapi.Param{}, repoParam...),
				hubapi.Param{Name: "start", In: "body", Type: "string", Required: true, Description: "Start as an RFC 3339 timestamp or a YYYY-MM-DD date."}),
			CLINames: []string{"milestone-set-start"},
			Response: "MilestoneSchedule", ResponseIs: "object",
		},
		Handler: SetMilestoneSchedule,
	}
}

func clearMilestoneScheduleEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "clearMilestoneSchedule", Method: http.MethodDelete, Path: "/milestones/{milestone_id}/schedule",
			Summary:     "Clear a milestone's start date",
			Description: "Same authorization as setting it.",
			Tag:         "issues", PathParams: milestoneParam,
			Body:     append([]hubapi.Param{}, repoParam...),
			CLINames: []string{"milestone-clear-start"},
			Response: "MilestoneSchedule", ResponseIs: "object",
		},
		Handler: ClearMilestoneSchedule,
	}
}

func setIssueEstimateEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "setIssueEstimate", Method: http.MethodPut, Path: "/issues/{issue_id}/estimate",
			Summary: "Set an issue's time estimate",
			Description: "time_estimate is a duration like \"3d\" or \"4h30m\", parsed and written through Gitea's own " +
				"time-tracking. Authorized by Gitea's own write check on the Issues unit.",
			Tag: "issues", PathParams: issueParam,
			Body: append(append([]hubapi.Param{}, repoParam...),
				hubapi.Param{Name: "time_estimate", In: "body", Type: "string", Required: true, Description: "Duration such as \"3d\" or \"4h30m\"."}),
			CLINames: []string{"issue-set-estimate"},
			Response: "IssueFacets", ResponseIs: "object",
		},
		Handler: SetIssueEstimate,
	}
}

// GetIssueFacets answers GET /issues/{issue_id}.
func GetIssueFacets(ctx *context.APIContext) {
	issue, repo, ok := readableIssue(ctx)
	if !ok {
		return
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	facets, ok := issueFacets(ctx, issue, perm.CanWriteIssuesOrPulls(issue.IsPull))
	if !ok {
		return
	}
	ctx.JSON(http.StatusOK, facets)
}

// SetIssueSchedule answers PUT /issues/{issue_id}/schedule.
func SetIssueSchedule(ctx *context.APIContext) {
	body, _, issue, ok := issueTarget(ctx)
	if !ok {
		return
	}
	start, ok := requiredDate(ctx, body.Start)
	if !ok {
		return
	}
	if err := planning_service.SetIssueStart(ctx, issue, time.Unix(start, 0).UTC()); err != nil {
		hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
		return
	}
	renderIssueFacets(ctx, issue)
}

// ClearIssueSchedule answers DELETE /issues/{issue_id}/schedule.
func ClearIssueSchedule(ctx *context.APIContext) {
	_, _, issue, ok := issueTarget(ctx)
	if !ok {
		return
	}
	if err := planning_service.ClearIssueStart(ctx, issue.ID); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	renderIssueFacets(ctx, issue)
}

// SetMilestoneSchedule answers PUT /milestones/{milestone_id}/schedule.
func SetMilestoneSchedule(ctx *context.APIContext) {
	body, repo, ok := writeTarget(ctx)
	if !ok {
		return
	}
	milestone, ok := repoMilestone(ctx, repo)
	if !ok {
		return
	}
	start, ok := requiredDate(ctx, body.Start)
	if !ok {
		return
	}
	if err := planning_service.SetMilestoneStart(ctx, milestone, time.Unix(start, 0).UTC()); err != nil {
		hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
		return
	}
	renderMilestoneSchedule(ctx, milestone)
}

// ClearMilestoneSchedule answers DELETE /milestones/{milestone_id}/schedule.
func ClearMilestoneSchedule(ctx *context.APIContext) {
	_, repo, ok := writeTarget(ctx)
	if !ok {
		return
	}
	milestone, ok := repoMilestone(ctx, repo)
	if !ok {
		return
	}
	if err := planning_service.ClearMilestoneStart(ctx, milestone.ID); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	renderMilestoneSchedule(ctx, milestone)
}

// SetIssueEstimate answers PUT /issues/{issue_id}/estimate.
func SetIssueEstimate(ctx *context.APIContext) {
	body, _, issue, ok := issueTarget(ctx)
	if !ok {
		return
	}
	seconds, err := util.TimeEstimateParse(body.TimeEstimate)
	if err != nil {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "bad_estimate",
			fmt.Sprintf("%q is not a time estimate this endpoint reads", body.TimeEstimate),
			`Send a duration like "3d" or "4h30m".`)
		return
	}
	if err := issue_service.ChangeTimeEstimate(ctx, issue, ctx.Doer, seconds); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	renderIssueFacets(ctx, issue)
}

// readableIssue resolves the issue the path names and confirms the doer can read it,
// answering 404 for both a missing issue and one the doer cannot see, so the refusal never
// confirms which is the case.
func readableIssue(ctx *context.APIContext) (*issues_model.Issue, *repo_model.Repository, bool) {
	issue, err := issues_model.GetIssueByID(ctx, ctx.PathParamInt64("issue_id"))
	if err != nil {
		hubapi.APIError(ctx, http.StatusNotFound, "issue_not_found",
			"no issue with that id is visible to you",
			"The path takes the issue's global id, not its per-repository number; "+BasePath+"/roadmap publishes issue_id on every bar.")
		return nil, nil, false
	}
	repo, err := repo_model.GetRepositoryByID(ctx, issue.RepoID)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, nil, false
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, nil, false
	}
	if !perm.CanReadIssuesOrPulls(issue.IsPull) {
		hubapi.APIError(ctx, http.StatusNotFound, "issue_not_found",
			"no issue with that id is visible to you",
			"The path takes the issue's global id, not its per-repository number; "+BasePath+"/roadmap publishes issue_id on every bar.")
		return nil, nil, false
	}
	return issue, repo, true
}

// repoMilestone resolves the milestone the path names, refusing one that does not belong to
// repo — a 422 rather than a 404, because the id is a real milestone, only not this one's.
func repoMilestone(ctx *context.APIContext, repo *repo_model.Repository) (*issues_model.Milestone, bool) {
	milestone, err := issues_model.GetMilestoneByRepoID(ctx, repo.ID, ctx.PathParamInt64("milestone_id"))
	if err != nil {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "milestone_not_in_repo",
			"no milestone with that id belongs to "+repo.FullName(),
			"Read the chart's rows from "+BasePath+"/roadmap and use one of their milestone_id values.")
		return nil, false
	}
	return milestone, true
}

// requiredDate parses a body's start field, refusing an empty one: unlike POST
// /issues/{issue_id}/dates, a schedule write has nothing else to leave unchanged.
func requiredDate(ctx *context.APIContext, raw string) (int64, bool) {
	if strings.TrimSpace(raw) == "" {
		hubapi.APIError(ctx, http.StatusBadRequest, "missing_start",
			"start is required to set a schedule",
			"Send start as an RFC 3339 timestamp or a YYYY-MM-DD date.")
		return 0, false
	}
	return parseDate(ctx, "start", raw)
}

// issueFacets builds the response GetIssueFacets and every issue-scoped write reply with.
func issueFacets(ctx *context.APIContext, issue *issues_model.Issue, canWrite bool) (*IssueFacets, bool) {
	if err := issue.LoadAttributes(ctx); err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}
	if err := issue.LoadTotalTimes(ctx); err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}
	starts, err := planning_service.IssueStarts(ctx, []int64{issue.ID})
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}
	facets := &IssueFacets{
		IssueID: issue.ID, Number: issue.Index, RepoID: issue.RepoID, CanWrite: canWrite,
		Schedule:       ScheduleFacet{StartUnix: int64(issue.CreatedUnix), StartSource: planning_service.StartFromCreated},
		TimeEstimate:   issue.TimeEstimate,
		TrackedSeconds: issue.TotalTrackedTime,
	}
	if start, ok := starts[issue.ID]; ok {
		facets.Schedule = ScheduleFacet{StartUnix: start, StartSource: planning_service.StartFromSchedule}
	}
	if issue.Milestone != nil {
		milestoneStarts, err := planning_service.MilestoneStarts(ctx, []int64{issue.Milestone.ID})
		if err != nil {
			ctx.APIErrorInternal(err)
			return nil, false
		}
		facets.Milestone = &MilestoneFacet{
			ID: issue.Milestone.ID, Title: issue.Milestone.Name,
			StartUnix: milestoneStarts[issue.Milestone.ID], DueUnix: int64(issue.Milestone.DeadlineUnix),
		}
	}
	return facets, true
}

// renderIssueFacets answers with the facets a write just changed. Every caller here has
// already passed the write check, so can_write is always true.
func renderIssueFacets(ctx *context.APIContext, issue *issues_model.Issue) {
	facets, ok := issueFacets(ctx, issue, true)
	if !ok {
		return
	}
	ctx.JSON(http.StatusOK, facets)
}

func renderMilestoneSchedule(ctx *context.APIContext, milestone *issues_model.Milestone) {
	starts, err := planning_service.MilestoneStarts(ctx, []int64{milestone.ID})
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusOK, &MilestoneSchedule{
		MilestoneID: milestone.ID, Title: milestone.Name,
		StartUnix: starts[milestone.ID], DueUnix: int64(milestone.DeadlineUnix),
	})
}
