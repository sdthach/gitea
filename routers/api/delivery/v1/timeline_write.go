// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	"gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/json"
	"gitea.dev/modules/optional"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"
	"gitea.dev/services/context"
	delivery_service "gitea.dev/services/delivery"
	issue_service "gitea.dev/services/issue"
)

// The timeline's writes. Every one goes through Gitea's own service layer as the calling
// user, so each leaves the record Gitea leaves for it — a milestone comment, a deadline
// comment, a new issue — and the fork keeps no second history of the same edits.
//
// A start date is the exception with no Gitea field behind it: Gitea stores no start, so a
// write posts the `ccpm:started=` comment the chart already reads, and nothing else in the
// repository is touched.

const maxTimelineBody = 16 << 10

// timelineWriteBody is every field the four writes take between them. One shape keeps the
// repository resolution and the permission check in one place.
type timelineWriteBody struct {
	Repo        string `json:"repo"`
	MilestoneID int64  `json:"milestone_id"`
	Start       string `json:"start"`
	End         string `json:"end"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Epic        string `json:"epic"`
}

var timelineRepoParam = []Param{
	{Name: "repo", In: "body", Type: "string", Required: true, Description: "Repository as owner/name."},
}

var timelineIssueParam = []Param{
	{Name: "issue_id", In: "path", Type: "integer", Required: true, Description: "The issue's global id, as the timeline's bars publish it."},
}

func moveTimelineIssueMilestoneEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "moveTimelineIssueMilestone", Method: http.MethodPost, Path: "/timeline/issues/{issue_id}/milestone",
			Summary: "Move an issue between the chart's milestone rows",
			Description: "Assigns the issue's milestone through Gitea's own ChangeMilestoneAssign, which records " +
				"the change as a milestone comment on the issue. Send milestone_id 0 to take the issue off every row. " +
				"Authorized by Gitea's own write check on the Issues unit (E10, I13).",
			Tag: "timeline", PathParams: timelineIssueParam,
			Body: append(append([]Param{}, timelineRepoParam...),
				Param{Name: "milestone_id", In: "body", Type: "integer", Description: "Target milestone; 0 removes the issue from its row."}),
			CLINames: []string{"timeline-move-milestone"},
			Response: "Timeline", ResponseIs: "object",
		},
		Handler: MoveTimelineIssueMilestone,
	}
}

func setTimelineIssueDatesEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "setTimelineIssueDates", Method: http.MethodPost, Path: "/timeline/issues/{issue_id}/dates",
			Summary: "Set a bar's start and end",
			Description: "The end is Issue.DeadlineUnix, written through Gitea's own update, which records a deadline " +
				"comment. Gitea stores no start, so the start is written as the `ccpm:started=` comment the chart reads " +
				"(O7, O8) — no file in the repository is touched. Send either field empty to leave it as it stands. " +
				"Authorized by Gitea's own write check on the Issues unit (E10, I13).",
			Tag: "timeline", PathParams: timelineIssueParam,
			Body: append(append([]Param{}, timelineRepoParam...),
				Param{Name: "start", In: "body", Type: "string", Description: "Bar start as an RFC 3339 timestamp or a YYYY-MM-DD date."},
				Param{Name: "end", In: "body", Type: "string", Description: "Bar end as an RFC 3339 timestamp or a YYYY-MM-DD date."}),
			CLINames: []string{"timeline-set-dates"},
			Response: "Timeline", ResponseIs: "object",
		},
		Handler: SetTimelineIssueDates,
	}
}

func createTimelineMilestoneEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "createTimelineMilestone", Method: http.MethodPost, Path: "/timeline/milestones",
			Summary:     "Create a milestone row",
			Description: "Creates the milestone the chart draws as a row. Authorized by Gitea's own write check on the Issues unit (E10, I13).",
			Tag:         "timeline",
			Body: append(append([]Param{}, timelineRepoParam...),
				Param{Name: "title", In: "body", Type: "string", Required: true, Description: "Milestone title."},
				Param{Name: "description", In: "body", Type: "string", Description: "Milestone description."},
				Param{Name: "end", In: "body", Type: "string", Description: "Milestone deadline as an RFC 3339 timestamp or a YYYY-MM-DD date."}),
			CLINames: []string{"timeline-create-milestone"},
			Response: "Timeline", ResponseIs: "object",
		},
		Handler: CreateTimelineMilestone,
	}
}

func createTimelineIssueEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "createTimelineIssue", Method: http.MethodPost, Path: "/timeline/issues",
			Summary: "Create an issue on a row",
			Description: "Creates the issue and files it under the milestone row and, when epic is given, the epic: label " +
				"the chart groups by — an issue with no epic: label is listed as unmanaged rather than drawn (O10). " +
				"Authorized by Gitea's own write check on the Issues unit (E10, I13).",
			Tag: "timeline",
			Body: append(append([]Param{}, timelineRepoParam...),
				Param{Name: "title", In: "body", Type: "string", Required: true, Description: "Issue title."},
				Param{Name: "description", In: "body", Type: "string", Description: "Issue body."},
				Param{Name: "milestone_id", In: "body", Type: "integer", Description: "Milestone row to file it under."},
				Param{Name: "epic", In: "body", Type: "string", Description: "Epic name; the issue is labelled epic:<name> so the chart draws it."}),
			CLINames: []string{"timeline-create-issue"},
			Response: "Timeline", ResponseIs: "object",
		},
		Handler: CreateTimelineIssue,
	}
}

// MoveTimelineIssueMilestone answers POST /timeline/issues/{issue_id}/milestone.
func MoveTimelineIssueMilestone(ctx *context.APIContext) {
	body, repo, issue, ok := timelineIssueTarget(ctx)
	if !ok {
		return
	}
	if body.MilestoneID != 0 && !timelineMilestoneBelongs(ctx, repo, body.MilestoneID) {
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
	renderTimelineAfterWrite(ctx, repo)
}

// SetTimelineIssueDates answers POST /timeline/issues/{issue_id}/dates.
func SetTimelineIssueDates(ctx *context.APIContext) {
	body, repo, issue, ok := timelineIssueTarget(ctx)
	if !ok {
		return
	}

	start, ok := parseTimelineDate(ctx, "start", body.Start)
	if !ok {
		return
	}
	end, ok := parseTimelineDate(ctx, "end", body.End)
	if !ok {
		return
	}
	if start == 0 && end == 0 {
		apiError(ctx, http.StatusBadRequest, "no_dates",
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
		// Gitea has no start field, so the record is the comment the chart already reads.
		if _, err := issue_service.CreateIssueComment(ctx, ctx.Doer, repo, issue,
			delivery_service.StartedMarkerComment(start), nil); err != nil {
			ctx.APIErrorInternal(err)
			return
		}
	}
	renderTimelineAfterWrite(ctx, repo)
}

// CreateTimelineMilestone answers POST /timeline/milestones.
func CreateTimelineMilestone(ctx *context.APIContext) {
	body, repo, ok := timelineWriteTarget(ctx)
	if !ok {
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		apiError(ctx, http.StatusBadRequest, "missing_title",
			"a milestone row needs a title", "Send title with the request.")
		return
	}
	deadline, ok := parseTimelineDate(ctx, "end", body.End)
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
	renderTimelineAfterWrite(ctx, repo)
}

// CreateTimelineIssue answers POST /timeline/issues.
func CreateTimelineIssue(ctx *context.APIContext) {
	body, repo, ok := timelineWriteTarget(ctx)
	if !ok {
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		apiError(ctx, http.StatusBadRequest, "missing_title",
			"an issue needs a title", "Send title with the request.")
		return
	}
	if body.MilestoneID != 0 && !timelineMilestoneBelongs(ctx, repo, body.MilestoneID) {
		return
	}

	var labelIDs []int64
	if epic := strings.TrimSpace(body.Epic); epic != "" {
		label, err := timelineEpicLabel(ctx, repo, epic)
		if err != nil {
			ctx.APIErrorInternal(err)
			return
		}
		labelIDs = []int64{label.ID}
	}

	issue := &issues_model.Issue{
		RepoID: repo.ID, Repo: repo, Title: title, Content: body.Description,
		PosterID: ctx.Doer.ID, Poster: ctx.Doer, MilestoneID: body.MilestoneID,
	}
	if err := issue_service.NewIssue(ctx, repo, issue, labelIDs, nil, nil, nil); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	renderTimelineAfterWrite(ctx, repo)
}

// timelineEpicLabel finds or creates the epic:<name> label the chart groups by, so creating
// an issue on an epic row does not silently produce an unmanaged one.
func timelineEpicLabel(ctx *context.APIContext, repo *repo_model.Repository, epic string) (*issues_model.Label, error) {
	name := delivery_service.EpicLabelPrefix + epic
	existing, err := issues_model.GetLabelInRepoByName(ctx, repo.ID, name)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, util.ErrNotExist) {
		return nil, err
	}
	label := &issues_model.Label{RepoID: repo.ID, Name: name, Color: "#ededed"}
	if err := issues_model.NewLabel(ctx, label); err != nil {
		return nil, err
	}
	return label, nil
}

// timelineWriteTarget reads the body, resolves the repository and applies Gitea's own write
// check on the Issues unit. Visibility is answered before permission, so a caller who cannot
// see the repository is not told it exists.
func timelineWriteTarget(ctx *context.APIContext) (*timelineWriteBody, *repo_model.Repository, bool) {
	body, ok := readTimelineWriteBody(ctx)
	if !ok {
		return nil, nil, false
	}
	owner, name, found := strings.Cut(strings.TrimSpace(body.Repo), "/")
	if !found || owner == "" || name == "" {
		apiError(ctx, http.StatusBadRequest, "bad_repo",
			fmt.Sprintf("repo must be owner/name, got %q", body.Repo),
			"Send repo as owner/name, for example \"acme/widgets\".")
		return nil, nil, false
	}
	repo, err := repo_model.GetRepositoryByOwnerAndName(ctx, owner, name)
	if err != nil {
		apiError(ctx, http.StatusNotFound, "repo_not_found",
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
		apiError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository "+owner+"/"+name+" is visible to you",
			"Check the owner and repository name against "+BasePath+"/repos.")
		return nil, nil, false
	}
	if !perm.CanWrite(unit.TypeIssues) {
		apiError(ctx, http.StatusForbidden, "forbidden",
			"your account has no write access to the Issues unit of "+repo.FullName(),
			"Ask a repository administrator for write permission on Issues.")
		return nil, nil, false
	}
	return body, repo, true
}

// timelineIssueTarget adds the issue the path names to what timelineWriteTarget resolves.
func timelineIssueTarget(ctx *context.APIContext) (*timelineWriteBody, *repo_model.Repository, *issues_model.Issue, bool) {
	body, repo, ok := timelineWriteTarget(ctx)
	if !ok {
		return nil, nil, nil, false
	}
	issue, err := issues_model.GetIssueByID(ctx, ctx.PathParamInt64("issue_id"))
	if err != nil || issue.RepoID != repo.ID {
		apiError(ctx, http.StatusNotFound, "issue_not_found",
			"no issue with that id belongs to "+repo.FullName(),
			"The path takes the issue's global id, not its per-repository number; "+BasePath+"/timeline publishes issue_id on every bar.")
		return nil, nil, nil, false
	}
	if err := issue.LoadAttributes(ctx); err != nil {
		ctx.APIErrorInternal(err)
		return nil, nil, nil, false
	}
	return body, repo, issue, true
}

func timelineMilestoneBelongs(ctx *context.APIContext, repo *repo_model.Repository, milestoneID int64) bool {
	milestone, err := issues_model.GetMilestoneByRepoID(ctx, repo.ID, milestoneID)
	if err != nil || milestone == nil {
		apiError(ctx, http.StatusUnprocessableEntity, "milestone_not_found",
			"no milestone with that id belongs to "+repo.FullName(),
			"Read the chart's rows from "+BasePath+"/timeline and use one of their milestone_id values.")
		return false
	}
	return true
}

// timelineDateFormats are what a date may be sent as: a full timestamp, or the day alone,
// which is what a chart's own controls produce.
var timelineDateFormats = []string{time.RFC3339, "2006-01-02"}

// parseTimelineDate returns 0 for an empty value, which every caller reads as "leave it".
func parseTimelineDate(ctx *context.APIContext, field, raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	for _, format := range timelineDateFormats {
		if at, err := time.Parse(format, raw); err == nil {
			return at.UTC().Unix(), true
		}
	}
	apiError(ctx, http.StatusBadRequest, "bad_date",
		fmt.Sprintf("%s is not a date this endpoint reads: %q", field, raw),
		"Send an RFC 3339 timestamp, for example 2026-03-01T00:00:00Z, or a YYYY-MM-DD date.")
	return 0, false
}

func readTimelineWriteBody(ctx *context.APIContext) (*timelineWriteBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(ctx.Req.Body, maxTimelineBody+1))
	if err != nil {
		apiError(ctx, http.StatusBadRequest, "unreadable_body",
			"the request body could not be read", "Retry the request.")
		return nil, false
	}
	if len(raw) > maxTimelineBody {
		apiError(ctx, http.StatusBadRequest, "body_too_large",
			"the request body is larger than 16KiB",
			"A timeline write is small; check whether the request is sending the right content.")
		return nil, false
	}
	body := new(timelineWriteBody)
	if err := json.Unmarshal(raw, body); err != nil {
		apiError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"repo": "acme/widgets", "milestone_id": 3}.`)
		return nil, false
	}
	return body, true
}

// renderTimelineAfterWrite answers with the chart the write produced, over the same
// projection GET serves, so a client never has to guess what its write did.
func renderTimelineAfterWrite(ctx *context.APIContext, repo *repo_model.Repository) {
	const limit = 200
	renderTimeline(ctx, repo, &issues_model.IssuesOptions{
		RepoIDs:   []int64{repo.ID},
		IsPull:    optional.Some(false),
		Paginator: &db.ListOptions{Page: 1, PageSize: limit},
		SortType:  "oldest",
	}, limit, true) // every caller here has already passed the write check
}
