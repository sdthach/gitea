// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"strconv"
	"testing"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/timeutil"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	issue_service "gitea.dev/services/issue"
	delivery_service "gitea.dev/services/planning"
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

// manageIssue puts an issue under an epic, which is what gives it a bar at all.
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
// it is the ccpm:started comment the chart already reads.
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
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")

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
	assert.Contains(t, names, "epic:checkout", "without the epic label the chart would list it as unmanaged")

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
			assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")
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

// TestDeliveryTimelineStartCanBeDraggedInBothDirections is the drag the chart offers: an
// edge moved right must move the bar's start right. The marker is append-only, so the read
// path decides which of an issue's markers the bar draws.
func TestDeliveryTimelineStartCanBeDraggedInBothDirections(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	issue := manageIssue(t, 5, "checkout")
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	after := timelineWrite(t, token, "/timeline/issues/5/dates",
		map[string]any{"repo": "user2/repo1", "start": "2026-03-10"})
	_, startUnix, _, _, _ := timelineBar(t, after, issue.ID)
	require.EqualValues(t, 1773100800, startUnix, "2026-03-10T00:00:00Z")

	// Drag the edge left: an earlier start.
	after = timelineWrite(t, token, "/timeline/issues/5/dates",
		map[string]any{"repo": "user2/repo1", "start": "2026-03-05"})
	_, startUnix, _, _, _ = timelineBar(t, after, issue.ID)
	assert.EqualValues(t, 1772668800, startUnix, "dragging the start edge left moves the bar left")

	// Drag the edge right: a later start.
	after = timelineWrite(t, token, "/timeline/issues/5/dates",
		map[string]any{"repo": "user2/repo1", "start": "2026-03-20"})
	_, startUnix, _, _, _ = timelineBar(t, after, issue.ID)
	assert.EqualValues(t, 1773964800, startUnix, "dragging the start edge right moves the bar right")
}

// getTimeline reads the chart the same way every client does, so the assertions below are
// over the published shape rather than over an internal one.
func getTimeline(t *testing.T, token, query string) timelinePayload {
	t.Helper()
	req := NewRequest(t, "GET", deliveryv1.BasePath+"/timeline?"+query).AddTokenAuth(token)
	var payload timelinePayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &payload)
	return payload
}

// labelIssue puts one existing label on an issue, which is how a fixture issue is given the
// type and epic ccpm would have written onto it.
func labelIssue(t *testing.T, issueID int64, label *issues_model.Label) {
	t.Helper()
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: issueID})
	require.NoError(t, issue_service.AddLabel(t.Context(), issue, doer, label))
}

// spanOf finds one rollup row on the chart.
func spanOf(t *testing.T, payload timelinePayload, kind, key string) int {
	t.Helper()
	for i, span := range payload.Spans {
		if span.Kind == kind && span.Key == key {
			return i
		}
	}
	t.Fatalf("the chart has no %s rollup for %q", kind, key)
	return -1
}

// TestAPIDeliveryTimelineFlagsAnEpicThatEndsBeforeItsChildrenAtEveryZoom is the check the
// chart is the only place to see, and the filter it is most needed under.
//
// At zoom=epic no child bar is drawn at all, so a rollup folded from the drawn bars would
// contain a set of nothing and the warning could never fire. The rows come from their own
// fetch, so the same epic is flagged with no child on screen.
func TestAPIDeliveryTimelineFlagsAnEpicThatEndsBeforeItsChildrenAtEveryZoom(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	epic := labelForLane(t, 1, delivery_service.EpicLabelPrefix+"checkout")
	labelIssue(t, 1, epic)
	labelIssue(t, 1, labelForLane(t, 1, delivery_service.TypeLabelPrefix+delivery_service.TypeEpic))
	labelIssue(t, 5, epic)
	labelIssue(t, 5, labelForLane(t, 1, delivery_service.TypeLabelPrefix+"story"))

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	// The epic declares 2026-03-01 to 2026-03-11; the story filed under it runs to 2026-03-25.
	timelineWrite(t, token, "/timeline/issues/1/dates",
		map[string]any{"repo": "user2/repo1", "start": "2026-03-01", "end": "2026-03-11"})
	timelineWrite(t, token, "/timeline/issues/5/dates",
		map[string]any{"repo": "user2/repo1", "start": "2026-03-01", "end": "2026-03-25"})

	atIssue := getTimeline(t, token, "repo_id=1&limit=200")
	row := atIssue.Spans[spanOf(t, atIssue, "epic", "checkout")]
	assert.Equal(t, 1, row.Children, "the epic issue is not one of its own children")
	assert.False(t, row.ContainsChildren)
	assert.EqualValues(t, 1773187200, row.DeclaredEndUnix, "2026-03-11, the epic's own deadline")
	assert.EqualValues(t, 1774396800, row.EndUnix, "2026-03-25, the story's")
	assert.Equal(t, "epic checkout (#1) ends 14 days before the work filed under it", row.Warning)
	assert.Equal(t, "Move the epic's deadline to 2026-03-25, or move story #4 earlier.", row.SuggestedAction)
	assert.EqualValues(t, 1, row.IssueID, "the row names the epic issue, so a bracket can be opened")

	atEpic := getTimeline(t, token, "repo_id=1&limit=200&zoom=epic")
	assert.Empty(t, atEpic.Bars, "a rolled-up zoom lists brackets, not the bars under them")
	assert.Equal(t, "epic", atEpic.Zoom)
	same := atEpic.Spans[spanOf(t, atEpic, "epic", "checkout")]
	assert.Equal(t, row.Warning, same.Warning, "the same epic is flagged where no child is drawn")
	assert.Equal(t, row.SuggestedAction, same.SuggestedAction)
	assert.Equal(t, 1, same.Children)

	// The ruler follows what is drawn: three weeks and a bit is a week ruler.
	assert.Equal(t, "week", atEpic.Ruler.Unit)
	require.NotEmpty(t, atEpic.Ruler.Ticks)
	assert.Equal(t, "w/c 23 Feb", atEpic.Ruler.Ticks[0].Label, "the axis starts on a unit boundary in UTC")

	// Moving the epic's deadline past its children clears the warning; it is a warning, not
	// a refusal, and it goes away when the schedule stops contradicting itself.
	timelineWrite(t, token, "/timeline/issues/1/dates",
		map[string]any{"repo": "user2/repo1", "end": "2026-03-25"})
	fixed := getTimeline(t, token, "repo_id=1&limit=200&zoom=epic")
	contained := fixed.Spans[spanOf(t, fixed, "epic", "checkout")]
	assert.True(t, contained.ContainsChildren)
	assert.Empty(t, contained.Warning)
}

// TestAPIDeliveryTimelineLaneMoveWritesWhatTheBoardsLaneMoveWrites is what makes the chart's
// vertical drag and the board's lane move one operation: both go through PlanLaneMove, so
// both leave the same field on the issue.
func TestAPIDeliveryTimelineLaneMoveWritesWhatTheBoardsLaneMoveWrites(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	labelForLane(t, 1, delivery_service.TypeLabelPrefix+"bug")
	labelIssue(t, 1, labelForLane(t, 1, delivery_service.TypeLabelPrefix+"task"))
	manageIssue(t, 1, "checkout")
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	after := timelineWrite(t, token, "/timeline/issues/1/lane",
		map[string]any{"repo": "user2/repo1", "group_by": "type", "lane": "bug"})
	assert.Equal(t, "type", after.GroupBy)
	require.NotEmpty(t, after.Lanes)
	assert.Equal(t, "bug", after.Lanes[0].Key, "the chart answers with the lane the bar landed in")

	reloaded := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	require.NoError(t, reloaded.LoadLabels(t.Context()))
	names := make([]string, 0, len(reloaded.Labels))
	for _, label := range reloaded.Labels {
		names = append(names, label.Name)
	}
	assert.Contains(t, names, "type:bug", "the lane IS the label, so the move rewrote it")
	assert.NotContains(t, names, "type:task", "a bar never carries two labels of the same grouping")

	// The same request against the board's own lane endpoint reaches the same field, which
	// is the point of there being one definition rather than two.
	req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/board/cards/1/lane",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "group_by": "type", "lane": "task"}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
	reloaded = unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	require.NoError(t, reloaded.LoadLabels(t.Context()))
	names = names[:0]
	for _, label := range reloaded.Labels {
		names = append(names, label.Name)
	}
	assert.Contains(t, names, "type:task")
	assert.NotContains(t, names, "type:bug")

	// Grouping off leaves no field to write, so the move is refused rather than guessed at.
	req = NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/timeline/issues/1/lane",
		map[string]any{"repo": "user2/repo1", "group_by": "none", "lane": "bug"}).AddTokenAuth(token)
	var refusal deliveryRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusBadRequest), &refusal)
	assert.Contains(t, refusal.Message, "not grouped")
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")
}

// TestAPIDeliveryTimelineRefusesALaneMoveWithoutIssueWrite asserts the refusal AND that
// nothing was written: a 403 that had already written would be a worse defect than no guard.
// user4 can read user2/repo1, which is public, and cannot write its Issues unit.
func TestAPIDeliveryTimelineRefusesALaneMoveWithoutIssueWrite(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	labelForLane(t, 1, delivery_service.TypeLabelPrefix+"bug")
	labelIssue(t, 1, labelForLane(t, 1, delivery_service.TypeLabelPrefix+"task"))
	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/timeline/issues/1/lane",
		map[string]any{"repo": "user2/repo1", "group_by": "type", "lane": "bug"}).AddTokenAuth(outsiderToken)
	var refusal deliveryRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusForbidden), &refusal)
	assert.Equal(t, "forbidden", refusal.Code)
	assert.Contains(t, refusal.Message, "Issues")
	assert.Contains(t, refusal.Message, "user2/repo1")
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")

	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	require.NoError(t, issue.LoadLabels(t.Context()))
	names := make([]string, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		names = append(names, label.Name)
	}
	assert.Contains(t, names, "type:task", "the refused move left the lane where it was")
	assert.NotContains(t, names, "type:bug", "the refused move wrote no label")
}

// TestAPIDeliveryTimelineMarksARollupPartialPastThePageLimit is the shipped defect the second
// fetch also fixes: a rollup over more children than one page holds is a floor, and printing
// a progress percentage over it would present a prefix as a measurement.
func TestAPIDeliveryTimelineMarksARollupPartialPastThePageLimit(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	label := labelForLane(t, 1, delivery_service.EpicLabelPrefix+"wide")
	const children = 205
	issues := make([]*issues_model.Issue, 0, children)
	links := make([]*issues_model.IssueLabel, 0, children)
	for i := range children {
		id := int64(9000 + i)
		issues = append(issues, &issues_model.Issue{
			ID: id, RepoID: 1, Index: int64(9000 + i), PosterID: 2, Title: "wide",
			CreatedUnix: timeutil.TimeStamp(1772323200), UpdatedUnix: timeutil.TimeStamp(1772323200),
			IsClosed: i%2 == 0,
		})
		links = append(links, &issues_model.IssueLabel{IssueID: id, LabelID: label.ID})
	}
	require.NoError(t, db.Insert(t.Context(), issues))
	require.NoError(t, db.Insert(t.Context(), links))

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	payload := getTimeline(t, token, "repo_id=1&epic=wide&limit=200")

	assert.True(t, payload.Truncated, "the chart itself is a prefix and says so")
	row := payload.Spans[spanOf(t, payload, "epic", "wide")]
	assert.True(t, row.Partial, "the rollup hit its own cap")
	assert.Zero(t, row.Progress, "a fraction of an unknown denominator is not a measurement")
	assert.Equal(t, 200, row.Children)
}

// insertEpicIssues creates one epic issue and its children directly, so their creation order
// is controllable: oldest-first paging is what decides which of them a short page holds.
func insertEpicIssues(t *testing.T, epicLabel, typeLabel *issues_model.Label, firstID int64, childUnix []int64, epicUnix int64) {
	t.Helper()
	rows := make([]*issues_model.Issue, 0, len(childUnix)+1)
	links := make([]*issues_model.IssueLabel, 0, 2*len(childUnix)+2)
	add := func(id, at int64, epic bool) {
		rows = append(rows, &issues_model.Issue{
			ID: id, RepoID: 1, Index: id, PosterID: 2, Title: "paged",
			CreatedUnix: timeutil.TimeStamp(at), UpdatedUnix: timeutil.TimeStamp(at),
		})
		links = append(links, &issues_model.IssueLabel{IssueID: id, LabelID: epicLabel.ID})
		if epic {
			links = append(links, &issues_model.IssueLabel{IssueID: id, LabelID: typeLabel.ID})
		}
	}
	for i, at := range childUnix {
		add(firstID+int64(i), at, false)
	}
	add(firstID+int64(len(childUnix)), epicUnix, true)
	require.NoError(t, db.Insert(t.Context(), rows))
	require.NoError(t, db.Insert(t.Context(), links))
}

// TestAPIDeliveryTimelineAtEpicZoomPagesOverEpicsNotOverIssues: at zoom=epic the fetch selects
// epic issues, so a page of N holds N epics. Paging over every issue instead would drop an
// epic whose issues all sit past the limit, and truncated would be about issues, not epics.
func TestAPIDeliveryTimelineAtEpicZoomPagesOverEpicsNotOverIssues(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	typeEpic := labelForLane(t, 1, delivery_service.TypeLabelPrefix+delivery_service.TypeEpic)
	// Four children created before their epic issue, so oldest-first fills a short page with
	// them alone; the second epic is created after all of them.
	insertEpicIssues(t, labelForLane(t, 1, delivery_service.EpicLabelPrefix+"wideearly"), typeEpic,
		9100, []int64{1000, 1001, 1002, 1003}, 1004)
	insertEpicIssues(t, labelForLane(t, 1, delivery_service.EpicLabelPrefix+"latecomer"), typeEpic,
		9200, []int64{2001}, 2000)

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	payload := getTimeline(t, token, "repo_id=1&zoom=epic&limit=3")

	assert.Empty(t, payload.Bars, "a rolled-up zoom lists brackets, not the bars under them")
	assert.False(t, payload.Truncated, "two epics fit in a page of three")
	late := payload.Spans[spanOf(t, payload, "epic", "latecomer")]
	assert.Equal(t, 1, late.Children, "the epic every issue-paged page would have missed")
	early := payload.Spans[spanOf(t, payload, "epic", "wideearly")]
	assert.Equal(t, 4, early.Children, "its rollup still counts every child, page or no page")

	// The epic filter still narrows the chart to one epic at this zoom.
	one := getTimeline(t, token, "repo_id=1&zoom=epic&limit=3&epic=latecomer")
	require.Len(t, one.Spans, 1)
	assert.Equal(t, "latecomer", one.Spans[0].Key)
}

// TestAPIDeliveryTimelineListsAnEpicWithNoChildrenYet: an epic filed a minute ago has a
// window and nothing under it, and a chart that drew nothing for it would say nothing.
func TestAPIDeliveryTimelineListsAnEpicWithNoChildrenYet(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	labelIssue(t, 1, labelForLane(t, 1, delivery_service.EpicLabelPrefix+"lonely"))
	labelIssue(t, 1, labelForLane(t, 1, delivery_service.TypeLabelPrefix+delivery_service.TypeEpic))

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	payload := getTimeline(t, token, "repo_id=1&zoom=epic&limit=200")

	require.Len(t, payload.Spans, 1, "the epic is listed even with nothing filed under it")
	assert.Equal(t, "lonely", payload.Spans[0].Key)
	assert.Zero(t, payload.Spans[0].Children)
	assert.Zero(t, payload.Spans[0].Progress)
	assert.EqualValues(t, 1, payload.Spans[0].IssueID, "the bracket can still be opened")
	assert.True(t, payload.Spans[0].ContainsChildren)
	assert.Empty(t, payload.Spans[0].Warning)
	assert.Empty(t, payload.Bars)
	assert.Empty(t, payload.Lanes, "a zoom that publishes no bar has no lanes to group them into")
}

// TestAPIDeliveryTimelineAtMilestoneZoomPagesOverMilestonesNotOverIssues: milestones are not
// issues, so a page of N holds N milestone rows. Paging over issues instead drops a milestone
// whose issues all sit past the limit.
func TestAPIDeliveryTimelineAtMilestoneZoomPagesOverMilestonesNotOverIssues(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	epic := labelForLane(t, 1, delivery_service.EpicLabelPrefix+"paged")
	// Four managed issues on no milestone, created before everything else, so oldest-first
	// fills a short page of ISSUES with them alone.
	rows := make([]*issues_model.Issue, 0, 5)
	links := make([]*issues_model.IssueLabel, 0, 5)
	for i := range 4 {
		id := int64(9300 + i)
		rows = append(rows, &issues_model.Issue{
			ID: id, RepoID: 1, Index: id, PosterID: 2, Title: "filler",
			CreatedUnix: timeutil.TimeStamp(1000 + int64(i)), UpdatedUnix: timeutil.TimeStamp(1000 + int64(i)),
		})
		links = append(links, &issues_model.IssueLabel{IssueID: id, LabelID: epic.ID})
	}
	require.NoError(t, db.Insert(t.Context(), rows))
	require.NoError(t, db.Insert(t.Context(), links))

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	// The milestone is created last and holds one managed issue created last.
	timelineWrite(t, token, "/timeline/milestones", map[string]any{"repo": "user2/repo1", "title": "Sprint 9"})
	sprint, err := issues_model.GetMilestoneByRepoIDANDName(t.Context(), 1, "Sprint 9")
	require.NoError(t, err)
	require.NoError(t, db.Insert(t.Context(), &issues_model.Issue{
		ID: 9400, RepoID: 1, Index: 9400, PosterID: 2, Title: "late", MilestoneID: sprint.ID,
		CreatedUnix: timeutil.TimeStamp(2_000_000_000), UpdatedUnix: timeutil.TimeStamp(2_000_000_000),
	}))
	require.NoError(t, db.Insert(t.Context(), &issues_model.IssueLabel{IssueID: 9400, LabelID: epic.ID}))

	milestones, err := unittest.GetXORMEngine().Where("repo_id = ?", 1).Count(new(issues_model.Milestone))
	require.NoError(t, err)
	payload := getTimeline(t, token, "repo_id=1&zoom=milestone&limit="+strconv.FormatInt(milestones+1, 10))

	assert.Empty(t, payload.Bars, "a rolled-up zoom lists brackets, not the bars under them")
	assert.False(t, payload.Truncated, "every milestone fits the page, whatever the issue count is")
	row := payload.Spans[spanOf(t, payload, "milestone", strconv.FormatInt(sprint.ID, 10))]
	assert.Equal(t, 1, row.Children, "the milestone every issue-paged page would have missed")

	// The milestone filter still narrows the chart at this zoom, where the page is over
	// milestones rather than over the issues it would otherwise have narrowed.
	one := getTimeline(t, token, "repo_id=1&zoom=milestone&limit=200&milestone_id="+strconv.FormatInt(sprint.ID, 10))
	require.Len(t, one.Spans, 1)
	assert.Equal(t, strconv.FormatInt(sprint.ID, 10), one.Spans[0].Key)
	assert.False(t, one.Truncated)
}

// TestAPIDeliveryTimelineAttachesAnArrowToTheBracketItsEndFallsIn: at a rolled-up zoom the
// bar an edge pointed at is not drawn, so the edge attaches to the bracket holding it rather
// than vanishing. The gate/sequence distinction survives the re-keying, and an edge with both
// ends in one bracket is dropped: it says nothing about the order of the brackets.
func TestAPIDeliveryTimelineAttachesAnArrowToTheBracketItsEndFallsIn(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	typeEpic := labelForLane(t, 1, delivery_service.TypeLabelPrefix+delivery_service.TypeEpic)
	insertEpicIssues(t, labelForLane(t, 1, delivery_service.EpicLabelPrefix+"checkout"), typeEpic,
		9500, []int64{1000, 1001}, 1002)
	insertEpicIssues(t, labelForLane(t, 1, delivery_service.EpicLabelPrefix+"billing"), typeEpic,
		9600, []int64{2000}, 2001)
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	// checkout's first child blocks billing's child: an edge across two brackets.
	require.NoError(t, db.Insert(t.Context(), &issues_model.IssueDependency{
		UserID: doer.ID, IssueID: 9600, DependencyID: 9500,
	}))
	// checkout's two children block each other: an edge inside one bracket.
	require.NoError(t, db.Insert(t.Context(), &issues_model.IssueDependency{
		UserID: doer.ID, IssueID: 9501, DependencyID: 9500,
	}))

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	payload := getTimeline(t, token, "repo_id=1&zoom=epic&limit=200")

	require.Len(t, payload.Arrows, 1, "the edge inside one bracket is dropped, the one across two is kept")
	arrow := payload.Arrows[0]
	assert.Equal(t, "epic:checkout", arrow.FromSpan, "the edge attaches to the bracket its end falls in")
	assert.Equal(t, "epic:billing", arrow.ToSpan)
	assert.EqualValues(t, 9500, arrow.FromIssueID, "the child issues are still named")
	assert.EqualValues(t, 9600, arrow.ToIssueID)
	assert.Equal(t, "depends_on", arrow.Kind)
	assert.True(t, arrow.Enforced, "the forge itself refuses the close, whatever zoom it is read at")

	narrowed := getTimeline(t, token, "repo_id=1&zoom=epic&limit=200&epic=billing")
	require.Len(t, narrowed.Spans, 1)
	assert.Empty(t, narrowed.Arrows, "an edge whose other end has no bracket on the page has nothing to attach to")

	// A sequencing hint between the same pair keeps its own kind: it is enforced by nothing,
	// and a chart that flattened the two would read a hint as a gate.
	billingChild := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 9600})
	billingChild.Content = "### Relations\n\nPredecessor #9501\n"
	_, err := db.GetEngine(t.Context()).ID(billingChild.ID).Cols("content").Update(billingChild)
	require.NoError(t, err)

	payload = getTimeline(t, token, "repo_id=1&zoom=epic&limit=200")
	kinds := map[string]bool{}
	for _, a := range payload.Arrows {
		require.Equal(t, "epic:checkout", a.FromSpan)
		require.Equal(t, "epic:billing", a.ToSpan)
		kinds[a.Kind] = a.Enforced
	}
	enforced, hasGate := kinds["depends_on"]
	require.True(t, hasGate)
	assert.True(t, enforced)
	sequencing, hasHint := kinds["predecessor"]
	require.True(t, hasHint, "the rendered cross-reference still draws a sequencing hint between the brackets")
	assert.False(t, sequencing, "sequencing is enforced by nothing")
}

// TestAPIDeliveryTimelineAtMilestoneZoomNarrowsChildrenNotMilestones: state names the state of
// the ISSUES, here as at every other zoom. Filtering the milestones by their own open/closed
// flag instead would hide an open milestone holding finished work.
func TestAPIDeliveryTimelineAtMilestoneZoomNarrowsChildrenNotMilestones(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	epic := labelForLane(t, 1, delivery_service.EpicLabelPrefix+"paged")
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	timelineWrite(t, token, "/timeline/milestones", map[string]any{"repo": "user2/repo1", "title": "Sprint 9"})
	sprint, err := issues_model.GetMilestoneByRepoIDANDName(t.Context(), 1, "Sprint 9")
	require.NoError(t, err)
	require.False(t, sprint.IsClosed, "the milestone itself is open; only the issue under it is closed")

	require.NoError(t, db.Insert(t.Context(), &issues_model.Issue{
		ID: 9700, RepoID: 1, Index: 9700, PosterID: 2, Title: "finished", MilestoneID: sprint.ID,
		IsClosed: true, ClosedUnix: timeutil.TimeStamp(1_700_000_000),
		CreatedUnix: timeutil.TimeStamp(1_600_000_000), UpdatedUnix: timeutil.TimeStamp(1_700_000_000),
	}))
	require.NoError(t, db.Insert(t.Context(), &issues_model.IssueLabel{IssueID: 9700, LabelID: epic.ID}))

	key := strconv.FormatInt(sprint.ID, 10)
	closed := getTimeline(t, token, "repo_id=1&zoom=milestone&limit=200&state=closed")
	row := closed.Spans[spanOf(t, closed, "milestone", key)]
	assert.Equal(t, 1, row.Children, "an open milestone holding closed work is listed at state=closed")
	assert.Equal(t, 100, row.Progress)

	// Its only issue is closed, so the same milestone has no open work and yields no row.
	open := getTimeline(t, token, "repo_id=1&zoom=milestone&limit=200&state=open")
	for _, span := range open.Spans {
		assert.NotEqual(t, key, span.Key, "a milestone whose children all fall outside the state is no row")
	}
}
