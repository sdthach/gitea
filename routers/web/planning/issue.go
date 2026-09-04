// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitea.dev/models/db"
	hub_model "gitea.dev/models/hub"
	issues_model "gitea.dev/models/issues"
	"gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/util"
	"gitea.dev/services/context"
	issue_service "gitea.dev/services/issue"
	planning_service "gitea.dev/services/planning"
)

// writableIssue resolves the issue the path names by its global id. A missing issue and one
// the doer cannot read both answer 404, so the refusal never confirms which repository it
// belongs to; one the doer can read but not write answers 403.
func writableIssue(ctx *context.Context) (*issues_model.Issue, bool) {
	issue, err := issues_model.GetIssueByID(ctx, ctx.PathParamInt64("id"))
	if err != nil {
		ctx.NotFound(nil)
		return nil, false
	}
	repo, err := repo_model.GetRepositoryByID(ctx, issue.RepoID)
	if err != nil {
		ctx.NotFound(nil)
		return nil, false
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.ServerError("GetDoerRepoPermission", err)
		return nil, false
	}
	if !perm.CanReadIssuesOrPulls(issue.IsPull) {
		ctx.NotFound(nil)
		return nil, false
	}
	if !perm.CanWriteIssuesOrPulls(issue.IsPull) {
		ctx.HTTPError(http.StatusForbidden)
		return nil, false
	}
	if err := issue.LoadAttributes(ctx); err != nil {
		ctx.ServerError("LoadAttributes", err)
		return nil, false
	}
	return issue, true
}

// writableMilestone resolves the milestone the path names by its global id, with the same
// 404-before-403 shape as writableIssue.
func writableMilestone(ctx *context.Context) (*issues_model.Milestone, bool) {
	milestone, exist, err := db.GetByID[issues_model.Milestone](ctx, ctx.PathParamInt64("id"))
	if err != nil {
		ctx.ServerError("GetByID", err)
		return nil, false
	}
	if !exist {
		ctx.NotFound(nil)
		return nil, false
	}
	repo, err := repo_model.GetRepositoryByID(ctx, milestone.RepoID)
	if err != nil {
		ctx.NotFound(nil)
		return nil, false
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.ServerError("GetDoerRepoPermission", err)
		return nil, false
	}
	if !perm.CanRead(unit.TypeIssues) {
		ctx.NotFound(nil)
		return nil, false
	}
	if !perm.CanWrite(unit.TypeIssues) {
		ctx.HTTPError(http.StatusForbidden)
		return nil, false
	}
	return milestone, true
}

// renderPlanningError answers a planning service's refusal: a *hub_model.Error surfaces as a
// JSON error carrying its own message and suggested action; anything else is a server error.
func renderPlanningError(ctx *context.Context, logMsg string, err error) {
	if hubErr, ok := errors.AsType[*hub_model.Error](err); ok {
		ctx.JSONError(hubErr.Error())
		return
	}
	ctx.ServerError(logMsg, err)
}

// dateOnly is the day format the schedule forms send, matching the roadmap's own controls.
const dateOnly = "2006-01-02"

// ScheduleIssue answers POST /planning/issues/{id}/schedule. An empty start clears it.
func ScheduleIssue(ctx *context.Context) {
	issue, ok := writableIssue(ctx)
	if !ok {
		return
	}
	start := strings.TrimSpace(ctx.FormString("start"))
	if start == "" {
		if err := planning_service.ClearIssueStart(ctx, issue.ID); err != nil {
			renderPlanningError(ctx, "ClearIssueStart", err)
			return
		}
		ctx.JSONRedirect("")
		return
	}
	at, err := time.Parse(dateOnly, start)
	if err != nil {
		ctx.JSONError("start must be a YYYY-MM-DD date")
		return
	}
	if err := planning_service.SetIssueStart(ctx, issue, at.UTC()); err != nil {
		renderPlanningError(ctx, "SetIssueStart", err)
		return
	}
	ctx.JSONRedirect("")
}

// TypeIssue answers POST /planning/issues/{id}/type. An empty type_id clears it.
func TypeIssue(ctx *context.Context) {
	issue, ok := writableIssue(ctx)
	if !ok {
		return
	}
	raw := strings.TrimSpace(ctx.FormString("type_id"))
	if raw == "" {
		if err := planning_service.ClearIssueType(ctx, issue.ID); err != nil {
			renderPlanningError(ctx, "ClearIssueType", err)
			return
		}
		ctx.JSONRedirect("")
		return
	}
	typeID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		ctx.JSONError("type_id must be a number")
		return
	}
	if err := planning_service.SetIssueType(ctx, issue, typeID); err != nil {
		renderPlanningError(ctx, "SetIssueType", err)
		return
	}
	ctx.JSONRedirect("")
}

// ParentIssue answers POST /planning/issues/{id}/parent. parent is "#N", "N" or empty, where N
// is the parent's own number in this issue's repository, not its global id.
func ParentIssue(ctx *context.Context) {
	issue, ok := writableIssue(ctx)
	if !ok {
		return
	}
	raw := strings.TrimSpace(ctx.FormString("parent"))
	if raw == "" {
		if err := planning_service.RemoveIssueParent(ctx, issue.ID); err != nil {
			renderPlanningError(ctx, "RemoveIssueParent", err)
			return
		}
		ctx.JSONRedirect("")
		return
	}
	index, err := strconv.ParseInt(strings.TrimPrefix(raw, "#"), 10, 64)
	if err != nil {
		ctx.JSONError("parent must be #N or N, the parent's number in this repository")
		return
	}
	parent, err := issues_model.GetIssueByIndex(ctx, issue.RepoID, index)
	if err != nil {
		ctx.JSONError("no issue with that number exists in this repository")
		return
	}
	if err := planning_service.SetIssueParent(ctx, issue, parent); err != nil {
		renderPlanningError(ctx, "SetIssueParent", err)
		return
	}
	ctx.JSONRedirect("")
}

// fieldValuesFromForm collects every field_<key> form member into the map SetIssueFields
// takes, so the write is one partial update rather than one call per field.
func fieldValuesFromForm(ctx *context.Context) map[string]any {
	if ctx.Req.Form == nil {
		_ = ctx.Req.ParseMultipartForm(32 << 20)
	}
	values := make(map[string]any, len(ctx.Req.Form))
	for key, vals := range ctx.Req.Form {
		fieldKey, ok := strings.CutPrefix(key, "field_")
		if !ok || len(vals) == 0 {
			continue
		}
		values[fieldKey] = vals[0]
	}
	return values
}

// FieldsIssue answers POST /planning/issues/{id}/fields: every field_<key> member is applied
// as one partial SetIssueFields update.
func FieldsIssue(ctx *context.Context) {
	issue, ok := writableIssue(ctx)
	if !ok {
		return
	}
	if err := planning_service.SetIssueFields(ctx, issue, fieldValuesFromForm(ctx)); err != nil {
		renderPlanningError(ctx, "SetIssueFields", err)
		return
	}
	ctx.JSONRedirect("")
}

// EstimateIssue answers POST /planning/issues/{id}/estimate.
func EstimateIssue(ctx *context.Context) {
	issue, ok := writableIssue(ctx)
	if !ok {
		return
	}
	seconds, err := util.TimeEstimateParse(strings.TrimSpace(ctx.FormString("time_estimate")))
	if err != nil {
		ctx.JSONError(`time_estimate must be a duration like "8h" or "4h30m"`)
		return
	}
	if err := issue_service.ChangeTimeEstimate(ctx, issue, ctx.Doer, seconds); err != nil {
		ctx.ServerError("ChangeTimeEstimate", err)
		return
	}
	ctx.JSONRedirect("")
}

// ScheduleMilestone answers POST /planning/milestones/{id}/schedule. An empty start clears it.
func ScheduleMilestone(ctx *context.Context) {
	milestone, ok := writableMilestone(ctx)
	if !ok {
		return
	}
	start := strings.TrimSpace(ctx.FormString("start"))
	if start == "" {
		if err := planning_service.ClearMilestoneStart(ctx, milestone.ID); err != nil {
			renderPlanningError(ctx, "ClearMilestoneStart", err)
			return
		}
		ctx.JSONRedirect("")
		return
	}
	at, err := time.Parse(dateOnly, start)
	if err != nil {
		ctx.JSONError("start must be a YYYY-MM-DD date")
		return
	}
	if err := planning_service.SetMilestoneStart(ctx, milestone, at.UTC()); err != nil {
		renderPlanningError(ctx, "SetMilestoneStart", err)
		return
	}
	ctx.JSONRedirect("")
}
