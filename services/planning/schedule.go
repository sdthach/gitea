// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"context"
	"net/http"
	"time"

	hub_model "gitea.dev/models/hub"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"
)

// SetIssueStart records issue's start, refusing a pull request, a start at or before the
// Unix epoch, and a start after the issue's own deadline when one is set.
func SetIssueStart(ctx context.Context, issue *issues_model.Issue, start time.Time) error {
	if issue.IsPull {
		return &hub_model.Error{
			Code: "not_an_issue", Status: http.StatusUnprocessableEntity,
			Message:         "a pull request has no schedule to write; the roadmap draws issues only",
			SuggestedAction: "Set the start on the underlying issue, not the pull request.",
		}
	}
	startUnix := start.Unix()
	if startUnix <= 0 {
		return &hub_model.Error{
			Code: "bad_start", Status: http.StatusBadRequest,
			Message:         "start must be after the Unix epoch",
			SuggestedAction: "Send a start after 1970-01-01T00:00:00Z, as an RFC 3339 timestamp or a YYYY-MM-DD date.",
		}
	}
	if issue.DeadlineUnix > 0 && startUnix > int64(issue.DeadlineUnix) {
		return &hub_model.Error{
			Code: "start_after_end", Status: http.StatusUnprocessableEntity,
			Message:         "start is after the issue's own deadline",
			SuggestedAction: "Choose a start on or before " + utcDay(int64(issue.DeadlineUnix)) + ", or move the deadline out.",
		}
	}
	return planning_model.UpsertIssueStart(ctx, issue.ID, startUnix)
}

// ClearIssueStart removes issueID's recorded start, if any.
func ClearIssueStart(ctx context.Context, issueID int64) error {
	return planning_model.DeleteIssueStart(ctx, issueID)
}

// SetMilestoneStart records milestone's start, refusing a start at or before the Unix epoch
// and a start after the milestone's own due date when one is set.
func SetMilestoneStart(ctx context.Context, milestone *issues_model.Milestone, start time.Time) error {
	startUnix := start.Unix()
	if startUnix <= 0 {
		return &hub_model.Error{
			Code: "bad_start", Status: http.StatusBadRequest,
			Message:         "start must be after the Unix epoch",
			SuggestedAction: "Send a start after 1970-01-01T00:00:00Z, as an RFC 3339 timestamp or a YYYY-MM-DD date.",
		}
	}
	if milestone.DeadlineUnix > 0 && startUnix > int64(milestone.DeadlineUnix) {
		return &hub_model.Error{
			Code: "start_after_end", Status: http.StatusUnprocessableEntity,
			Message:         "start is after the milestone's own due date",
			SuggestedAction: "Choose a start on or before " + utcDay(int64(milestone.DeadlineUnix)) + ", or move the milestone's due date out.",
		}
	}
	return planning_model.UpsertMilestoneStart(ctx, milestone.ID, startUnix)
}

// ClearMilestoneStart removes milestoneID's recorded start, if any.
func ClearMilestoneStart(ctx context.Context, milestoneID int64) error {
	return planning_model.DeleteMilestoneStart(ctx, milestoneID)
}

// IssueStarts is a thin wrapper over the model, so callers outside models/planning read
// starts through this package alone.
func IssueStarts(ctx context.Context, issueIDs []int64) (map[int64]int64, error) {
	return planning_model.IssueStarts(ctx, issueIDs)
}

// MilestoneStarts is IssueStarts' milestone counterpart.
func MilestoneStarts(ctx context.Context, milestoneIDs []int64) (map[int64]int64, error) {
	return planning_model.MilestoneStarts(ctx, milestoneIDs)
}
