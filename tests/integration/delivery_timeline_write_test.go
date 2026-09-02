// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	auth_model "gitea.dev/models/auth"
	issues_model "gitea.dev/models/issues"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	delivery_service "gitea.dev/services/delivery"
	issue_service "gitea.dev/services/issue"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// timelineWrite posts one write and decodes the chart it answers with. Every write replies
// through the read projection, so the assertions read the same shape GET produces.
func timelineWrite(t *testing.T, token, path string, body map[string]any) timelinePayload {
	t.Helper()
	req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+path, body).AddTokenAuth(token)
	var payload timelinePayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &payload)
	return payload
}

func timelineBar(t *testing.T, payload timelinePayload, issueID int64) (int64, int64, int64, string, string) {
	t.Helper()
	for _, bar := range payload.Bars {
		if bar.IssueID == issueID {
			return bar.MilestoneID, bar.StartUnix, bar.EndUnix, bar.StartSource, bar.EndSource
		}
	}
	t.Fatalf("issue %d has no bar on the chart", issueID)
	return 0, 0, 0, "", ""
}

// manageIssue puts an issue under an epic, which is what gives it a bar at all (O10).
func manageIssue(t *testing.T, issueID int64, epicName string) *issues_model.Issue {
	t.Helper()
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: issueID})
	label := labelForLane(t, issue.RepoID, delivery_service.EpicLabelPrefix+epicName)
	require.NoError(t, issue_service.AddLabel(t.Context(), issue, doer, label))
	return issue
}

// TestDeliveryTimelineMovesAnIssueBetweenMilestoneRows is the chart's first write. The move
// goes through Gitea's own ChangeMilestoneAssign, so it leaves the milestone comment Gitea
// leaves for one and the fork keeps no second history of the same edit.
func TestDeliveryTimelineMovesAnIssueBetweenMilestoneRows(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	issue := manageIssue(t, 1, "checkout")
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	after := timelineWrite(t, token, "/timeline/issues/1/milestone",
		map[string]any{"repo": "user2/repo1", "milestone_id": 2})
	milestoneID, _, _, _, _ := timelineBar(t, after, issue.ID)
	assert.EqualValues(t, 2, milestoneID, "the bar comes back on its new row")

	comments, err := issues_model.FindComments(t.Context(), &issues_model.FindCommentsOptions{
		IssueID: issue.ID, Type: issues_model.CommentTypeMilestone,
	})
	require.NoError(t, err)
	require.NotEmpty(t, comments, "the move is recorded on the issue by Gitea's own service")
	assert.EqualValues(t, 2, comments[len(comments)-1].MilestoneID)

	// 0 takes the issue off every row, which is how a bar leaves the chart's grouping.
	after = timelineWrite(t, token, "/timeline/issues/1/milestone",
		map[string]any{"repo": "user2/repo1", "milestone_id": 0})
	milestoneID, _, _, _, _ = timelineBar(t, after, issue.ID)
	assert.Zero(t, milestoneID)
}

// TestDeliveryTimelineSetsABarsStartAndEnd covers the endpoint whose two halves are written
// to different places: the end is Issue.DeadlineUnix, and the start has no Gitea field, so
// it is the ccpm:started comment the chart already reads (O7, O8).
func TestDeliveryTimelineSetsABarsStartAndEnd(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	issue := manageIssue(t, 5, "checkout")
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	after := timelineWrite(t, token, "/timeline/issues/5/dates", map[string]any{
		"repo": "user2/repo1", "start": "2026-03-01", "end": "2026-03-11T00:00:00Z",
	})
	_, startUnix, endUnix, startSource, endSource := timelineBar(t, after, issue.ID)

	assert.Equal(t, "ccpm_started", startSource, "the chart reads back the marker the write posted")
	assert.EqualValues(t, 1772323200, startUnix, "2026-03-01T00:00:00Z")
	assert.Equal(t, "deadline", endSource)
	assert.EqualValues(t, 1773187200, endUnix, "2026-03-11T00:00:00Z")

	reloaded := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 5})
	assert.EqualValues(t, 1773187200, reloaded.DeadlineUnix, "the end is Gitea's own deadline field")

	// Sending neither is refused rather than silently doing nothing.
	req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/timeline/issues/5/dates",
		map[string]any{"repo": "user2/repo1"}).AddTokenAuth(token)
	var refusal deliveryRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusBadRequest), &refusal)
	assert.Equal(t, "no_dates", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action (A21)")

	req = NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/timeline/issues/5/dates",
		map[string]any{"repo": "user2/repo1", "start": "last tuesday"}).AddTokenAuth(token)
	DecodeJSON(t, MakeRequest(t, req, http.StatusBadRequest), &refusal)
	assert.Equal(t, "bad_date", refusal.Code)
}

// TestDeliveryTimelineCreatesARowAndAnIssueOnIt covers the two creating writes. An issue
// created without an epic would be listed as unmanaged rather than drawn, so the endpoint
// applies the epic: label the chart groups by.
func TestDeliveryTimelineCreatesARowAndAnIssueOnIt(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	timelineWrite(t, token, "/timeline/milestones", map[string]any{
		"repo": "user2/repo1", "title": "Sprint 9", "description": "a new row", "end": "2026-04-01",
	})
	milestone, err := issues_model.GetMilestoneByRepoIDANDName(t.Context(), 1, "Sprint 9")
	require.NoError(t, err)
	assert.EqualValues(t, 1775001600, milestone.DeadlineUnix, "2026-04-01T00:00:00Z")

	after := timelineWrite(t, token, "/timeline/issues", map[string]any{
		"repo": "user2/repo1", "title": "Wire the checkout", "description": "- Size: S",
		"milestone_id": milestone.ID, "epic": "checkout",
	})

	created := new(issues_model.Issue)
	found, err := unittest.GetXORMEngine().Where("repo_id = ? AND name = ?", 1, "Wire the checkout").Get(created)
	require.NoError(t, err)
	require.True(t, found, "the issue was created")

	milestoneID, _, _, _, _ := timelineBar(t, after, created.ID)
	assert.Equal(t, milestone.ID, milestoneID, "the new issue is drawn on the row it was filed under")

	require.NoError(t, created.LoadAttributes(t.Context()))
	names := make([]string, 0, len(created.Labels))
	for _, label := range created.Labels {
		names = append(names, label.Name)
	}
	assert.Contains(t, names, "epic:checkout", "without the epic label the chart would list it as unmanaged (O10)")

	// A milestone needs a title, and a title-less request is refused rather than creating a
	// nameless row.
	req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/timeline/milestones",
		map[string]any{"repo": "user2/repo1"}).AddTokenAuth(token)
	var refusal deliveryRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusBadRequest), &refusal)
	assert.Equal(t, "missing_title", refusal.Code)
}

// TestDeliveryTimelineTellsTheClientWhetherItMayEdit covers what the chart's controls are
// gated on: can_write, and the rows an issue can be filed under. A milestone holding no
// issue has no span, so without rows the chart could not name it as a destination.
func TestDeliveryTimelineTellsTheClientWhetherItMayEdit(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	writerToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	readerToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	var writerView, readerView timelinePayload
	req := NewRequest(t, "GET", deliveryv1.BasePath+"/timeline?repo_id=1&limit=200").AddTokenAuth(writerToken)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &writerView)
	req = NewRequest(t, "GET", deliveryv1.BasePath+"/timeline?repo_id=1&limit=200").AddTokenAuth(readerToken)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &readerView)

	assert.True(t, writerView.CanWrite, "user2 owns the repository")
	assert.False(t, readerView.CanWrite, "user4 can read it and cannot write its Issues unit")

	titles := make([]string, 0, len(writerView.Rows))
	for _, row := range writerView.Rows {
		titles = append(titles, row.Title)
	}
	assert.Contains(t, titles, "milestone2", "a milestone holding no issue is still a row an issue can move to")
	assert.Equal(t, titles, func() []string {
		out := make([]string, 0, len(readerView.Rows))
		for _, row := range readerView.Rows {
			out = append(out, row.Title)
		}
		return out
	}(), "the rows are what the repository has, not what the caller may write")
}

// TestDeliveryTimelineRefusesEveryWriteWithoutIssueWrite asserts the refusal AND that
// nothing was written: a 403 that had already written would be a worse defect than no guard.
// user4 can read user2/repo1, which is public, and cannot write its Issues unit.
func TestDeliveryTimelineRefusesEveryWriteWithoutIssueWrite(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	issue := manageIssue(t, 1, "checkout")
	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	before := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: issue.ID})
	milestoneCount, err := unittest.GetXORMEngine().Where("repo_id = ?", 1).Count(new(issues_model.Milestone))
	require.NoError(t, err)
	issueCount, err := unittest.GetXORMEngine().Where("repo_id = ?", 1).Count(new(issues_model.Issue))
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		path string
		body map[string]any
	}{
		{"milestone", "/timeline/issues/1/milestone", map[string]any{"repo": "user2/repo1", "milestone_id": 2}},
		{"dates", "/timeline/issues/1/dates", map[string]any{"repo": "user2/repo1", "end": "2026-03-11"}},
		{"create milestone", "/timeline/milestones", map[string]any{"repo": "user2/repo1", "title": "Sprint 9"}},
		{"create issue", "/timeline/issues", map[string]any{"repo": "user2/repo1", "title": "Wire it"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+tc.path, tc.body).AddTokenAuth(outsiderToken)
			var refusal deliveryRefusal
			DecodeJSON(t, MakeRequest(t, req, http.StatusForbidden), &refusal)
			assert.Equal(t, "forbidden", refusal.Code)
			assert.Contains(t, refusal.Message, "Issues")
			assert.Contains(t, refusal.Message, "user2/repo1")
			assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action (A21)")
		})
	}

	after := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: issue.ID})
	assert.Equal(t, before.MilestoneID, after.MilestoneID, "the refused move wrote no milestone")
	assert.Equal(t, before.DeadlineUnix, after.DeadlineUnix, "the refused write set no deadline")

	milestonesAfter, err := unittest.GetXORMEngine().Where("repo_id = ?", 1).Count(new(issues_model.Milestone))
	require.NoError(t, err)
	assert.Equal(t, milestoneCount, milestonesAfter, "the refused create made no milestone")
	issuesAfter, err := unittest.GetXORMEngine().Where("repo_id = ?", 1).Count(new(issues_model.Issue))
	require.NoError(t, err)
	assert.Equal(t, issueCount, issuesAfter, "the refused create made no issue")
}
