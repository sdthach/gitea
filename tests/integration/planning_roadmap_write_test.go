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
	planning_model "gitea.dev/models/planning"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/timeutil"
	planningv1 "gitea.dev/routers/api/planning/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roadmapWrite posts one write and decodes the chart it answers with. Every write replies
// through the read projection, so the assertions read the same shape GET produces.
func roadmapWrite(t *testing.T, token, path string, body map[string]any) roadmapPayload {
	t.Helper()
	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+path, body).AddTokenAuth(token)
	var payload roadmapPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &payload)
	return payload
}

func roadmapBar(t *testing.T, payload roadmapPayload, issueID int64) (int64, int64, int64, string, string) {
	t.Helper()
	for _, bar := range payload.Bars {
		if bar.IssueID == issueID {
			return bar.MilestoneID, bar.StartUnix, bar.EndUnix, bar.StartSource, bar.EndSource
		}
	}
	t.Fatalf("issue %d has no bar on the chart", issueID)
	return 0, 0, 0, "", ""
}

// manageIssue assigns issueID a type directly, which is what makes it managed enough to draw
// a bar — replacing the epic:<name> label convention. A distinct name per call keeps type-
// grouping assertions elsewhere from tripping over a shared one.
func manageIssue(t *testing.T, issueID int64) *issues_model.Issue {
	t.Helper()
	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: issueID})
	ty := issueType(t, issue.RepoID, "managed"+strconv.FormatInt(issueID, 10), "#112233", "octicon-issue-opened", 3)
	require.NoError(t, planning_model.UpsertAssignment(t.Context(), issueID, ty.ID))
	return issue
}

// issueType creates a repo-scoped type directly, which is what an admin's own POST
// /issue-types would have left behind before this test's issue ever needed one.
func issueType(t *testing.T, repoID int64, name, color, icon string, rank int) *planning_model.IssueType {
	t.Helper()
	row := &planning_model.IssueType{RepoID: repoID, Name: name, Color: color, Icon: icon, Rank: rank}
	require.NoError(t, planning_model.InsertIssueType(t.Context(), row))
	return row
}

// setIssueType assigns typeID to issueID through the endpoint under test, the one a real
// client calls.
func setIssueType(t *testing.T, token, repo string, issueID, typeID int64) {
	t.Helper()
	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/"+strconv.FormatInt(issueID, 10)+"/type",
		map[string]any{"repo": repo, "type_id": typeID}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
}

// setIssueParent links childID under parentID through the endpoint under test.
func setIssueParent(t *testing.T, token, repo string, childID, parentID int64) {
	t.Helper()
	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/"+strconv.FormatInt(childID, 10)+"/parent",
		map[string]any{"repo": repo, "parent_issue_id": parentID}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
}

// TestPlanningRoadmapMovesAnIssueBetweenMilestoneRows is the chart's first write. The move
// goes through Gitea's own ChangeMilestoneAssign, so it leaves the milestone comment Gitea
// leaves for one and the fork keeps no second history of the same edit.
func TestPlanningRoadmapMovesAnIssueBetweenMilestoneRows(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	issue := manageIssue(t, 1)
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	after := roadmapWrite(t, token, "/issues/1/milestone",
		map[string]any{"repo": "user2/repo1", "milestone_id": 2})
	milestoneID, _, _, _, _ := roadmapBar(t, after, issue.ID)
	assert.EqualValues(t, 2, milestoneID, "the bar comes back on its new row")

	comments, err := issues_model.FindComments(t.Context(), &issues_model.FindCommentsOptions{
		IssueID: issue.ID, Type: issues_model.CommentTypeMilestone,
	})
	require.NoError(t, err)
	require.NotEmpty(t, comments, "the move is recorded on the issue by Gitea's own service")
	assert.EqualValues(t, 2, comments[len(comments)-1].MilestoneID)

	// 0 takes the issue off every row, which is how a bar leaves the chart's grouping.
	after = roadmapWrite(t, token, "/issues/1/milestone",
		map[string]any{"repo": "user2/repo1", "milestone_id": 0})
	milestoneID, _, _, _, _ = roadmapBar(t, after, issue.ID)
	assert.Zero(t, milestoneID)
}

// TestPlanningRoadmapSetsABarsStartAndEnd covers the endpoint whose two halves are written
// to different places: the end is Issue.DeadlineUnix, and the start has no Gitea field, so
// it is the recorded schedule the chart already reads.
func TestPlanningRoadmapSetsABarsStartAndEnd(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	issue := manageIssue(t, 5)
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	after := roadmapWrite(t, token, "/issues/5/dates", map[string]any{
		"repo": "user2/repo1", "start": "2026-03-01", "end": "2026-03-11T00:00:00Z",
	})
	_, startUnix, endUnix, startSource, endSource := roadmapBar(t, after, issue.ID)

	assert.Equal(t, "schedule", startSource, "the chart reads back the schedule the write recorded")
	assert.EqualValues(t, 1772323200, startUnix, "2026-03-01T00:00:00Z")
	assert.Equal(t, "deadline", endSource)
	assert.EqualValues(t, 1773187200, endUnix, "2026-03-11T00:00:00Z")

	reloaded := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 5})
	assert.EqualValues(t, 1773187200, reloaded.DeadlineUnix, "the end is Gitea's own deadline field")

	// Sending neither is refused rather than silently doing nothing.
	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issues/5/dates",
		map[string]any{"repo": "user2/repo1"}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusBadRequest), &refusal)
	assert.Equal(t, "no_dates", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")

	req = NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issues/5/dates",
		map[string]any{"repo": "user2/repo1", "start": "last tuesday"}).AddTokenAuth(token)
	DecodeJSON(t, MakeRequest(t, req, http.StatusBadRequest), &refusal)
	assert.Equal(t, "bad_date", refusal.Code)
}

// TestPlanningRoadmapCreatesARowAndAnIssue covers the two creating writes. A freshly created
// issue carries no type, so it is listed as unmanaged rather than drawn — filing it under a
// parent is a separate PUT /issues/{id}/parent, once it has a type to be ranked by.
func TestPlanningRoadmapCreatesARowAndAnIssue(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	roadmapWrite(t, token, "/milestones", map[string]any{
		"repo": "user2/repo1", "title": "Sprint 9", "description": "a new row", "end": "2026-04-01",
	})
	milestone, err := issues_model.GetMilestoneByRepoIDANDName(t.Context(), 1, "Sprint 9")
	require.NoError(t, err)
	assert.EqualValues(t, 1775001600, milestone.DeadlineUnix, "2026-04-01T00:00:00Z")

	after := roadmapWrite(t, token, "/issues", map[string]any{
		"repo": "user2/repo1", "title": "Wire the checkout", "description": "- Size: S",
		"milestone_id": milestone.ID,
	})

	created := new(issues_model.Issue)
	found, err := unittest.GetXORMEngine().Where("repo_id = ? AND name = ?", 1, "Wire the checkout").Get(created)
	require.NoError(t, err)
	require.True(t, found, "the issue was created")

	found = false
	for _, item := range after.Unmanaged {
		if item.Number == created.Index {
			found = true
		}
	}
	assert.True(t, found, "a freshly created issue carries no type, so it is listed as unmanaged")

	// A milestone needs a title, and a title-less request is refused rather than creating a
	// nameless row.
	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/milestones",
		map[string]any{"repo": "user2/repo1"}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusBadRequest), &refusal)
	assert.Equal(t, "missing_title", refusal.Code)
}

// TestPlanningRoadmapCreatesAnIssueWithATypeAndAParent covers CreateIssue's own type_id and
// parent_issue_id: both are validated before anything is created, so a refused parent leaves
// no issue behind, and an accepted one shows on the new issue's own facets immediately.
func TestPlanningRoadmapCreatesAnIssueWithATypeAndAParent(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	epic := issueType(t, 1, "epic", "#8250df", "octicon-rocket", 1)
	story := issueType(t, 1, "story", "#2da44e", "octicon-tasklist", 3)
	setIssueType(t, token, "user2/repo1", 1, epic.ID)

	roadmapWrite(t, token, "/issues", map[string]any{
		"repo": "user2/repo1", "title": "Wire the checkout", "type_id": story.ID, "parent_issue_id": 1,
	})

	created := new(issues_model.Issue)
	found, err := unittest.GetXORMEngine().Where("repo_id = ? AND name = ?", 1, "Wire the checkout").Get(created)
	require.NoError(t, err)
	require.True(t, found, "the issue was created")

	facets := getIssueFacets(t, token, created.ID)
	require.NotNil(t, facets.Parent)
	assert.EqualValues(t, 1, facets.Parent.IssueID, "the facets show the recorded parent")
	unittest.AssertExistsAndLoadBean(t, &planning_model.IssueTypeAssignment{IssueID: created.ID, TypeID: story.ID})

	// A refused parent creates no issue: an epic cannot be filed under a story (rank 3 does
	// not outrank rank 1), so the whole create is refused before anything is written.
	before, err := unittest.GetXORMEngine().Where("repo_id = ?", 1).Count(new(issues_model.Issue))
	require.NoError(t, err)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issues", map[string]any{
		"repo": "user2/repo1", "title": "Should not exist", "type_id": epic.ID, "parent_issue_id": created.ID,
	}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "rank_mismatch", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)

	after, err := unittest.GetXORMEngine().Where("repo_id = ?", 1).Count(new(issues_model.Issue))
	require.NoError(t, err)
	assert.Equal(t, before, after, "the refused create made no issue")
}

// TestPlanningRoadmapCreateRefusesAnUntypedParentAndAParentWithoutAType covers the two
// untyped_issue shapes: a parent that carries no type, and a parent given with no type_id for
// the new issue, which names the new issue as the untyped side.
func TestPlanningRoadmapCreateRefusesAnUntypedParentAndAParentWithoutAType(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	story := issueType(t, 1, "story", "#2da44e", "octicon-tasklist", 3)

	before, err := unittest.GetXORMEngine().Count(new(issues_model.Issue))
	require.NoError(t, err)

	// issue 1 carries no type in the fixtures.
	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issues", map[string]any{
		"repo": "user2/repo1", "title": "Should not exist", "type_id": story.ID, "parent_issue_id": 1,
	}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "untyped_issue", refusal.Code)
	assert.Contains(t, refusal.Message, "parent")

	epic := issueType(t, 1, "epic", "#8250df", "octicon-rocket", 1)
	setIssueType(t, token, "user2/repo1", 1, epic.ID)

	// A parent given without type_id names the NEW issue as the untyped side.
	req = NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issues", map[string]any{
		"repo": "user2/repo1", "title": "Should not exist", "parent_issue_id": 1,
	}).AddTokenAuth(token)
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "untyped_issue", refusal.Code)
	assert.Contains(t, refusal.Message, "new issue")

	after, err := unittest.GetXORMEngine().Count(new(issues_model.Issue))
	require.NoError(t, err)
	assert.Equal(t, before, after, "neither refused create made an issue")
}

// TestPlanningRoadmapTellsTheClientWhetherItMayEdit covers what the chart's controls are
// gated on: can_write, and the rows an issue can be filed under. A milestone holding no
// issue has no rollup, so without rows the chart could not name it as a destination.
func TestPlanningRoadmapTellsTheClientWhetherItMayEdit(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	writerToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	readerToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	var writerView, readerView roadmapPayload
	req := NewRequest(t, "GET", planningv1.BasePath+"/roadmap?repo_id=1&limit=200").AddTokenAuth(writerToken)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &writerView)
	req = NewRequest(t, "GET", planningv1.BasePath+"/roadmap?repo_id=1&limit=200").AddTokenAuth(readerToken)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &readerView)

	assert.True(t, writerView.CanWrite, "user2 owns the repository")
	assert.False(t, readerView.CanWrite, "user4 can read it and cannot write its Issues unit")

	titles := make([]string, 0, len(writerView.Milestones))
	for _, row := range writerView.Milestones {
		titles = append(titles, row.Title)
	}
	assert.Contains(t, titles, "milestone2", "a milestone holding no issue is still a row an issue can move to")
	assert.Equal(t, titles, func() []string {
		out := make([]string, 0, len(readerView.Milestones))
		for _, row := range readerView.Milestones {
			out = append(out, row.Title)
		}
		return out
	}(), "the rows are what the repository has, not what the caller may write")
}

// TestPlanningRoadmapRefusesEveryWriteWithoutIssueWrite asserts the refusal AND that
// nothing was written: a 403 that had already written would be a worse defect than no guard.
// user4 can read user2/repo1, which is public, and cannot write its Issues unit.
func TestPlanningRoadmapRefusesEveryWriteWithoutIssueWrite(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	issue := manageIssue(t, 1)
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
		{"milestone", "/issues/1/milestone", map[string]any{"repo": "user2/repo1", "milestone_id": 2}},
		{"dates", "/issues/1/dates", map[string]any{"repo": "user2/repo1", "end": "2026-03-11"}},
		{"create milestone", "/milestones", map[string]any{"repo": "user2/repo1", "title": "Sprint 9"}},
		{"create issue", "/issues", map[string]any{"repo": "user2/repo1", "title": "Wire it"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := NewRequestWithJSON(t, "POST", planningv1.BasePath+tc.path, tc.body).AddTokenAuth(outsiderToken)
			var refusal hubRefusal
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

// TestPlanningRoadmapStartCanBeDraggedInBothDirections is the drag the chart offers: an
// edge moved right must move the bar's start right. The marker is append-only, so the read
// path decides which of an issue's markers the bar draws.
func TestPlanningRoadmapStartCanBeDraggedInBothDirections(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	issue := manageIssue(t, 5)
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	after := roadmapWrite(t, token, "/issues/5/dates",
		map[string]any{"repo": "user2/repo1", "start": "2026-03-10"})
	_, startUnix, _, _, _ := roadmapBar(t, after, issue.ID)
	require.EqualValues(t, 1773100800, startUnix, "2026-03-10T00:00:00Z")

	// Drag the edge left: an earlier start.
	after = roadmapWrite(t, token, "/issues/5/dates",
		map[string]any{"repo": "user2/repo1", "start": "2026-03-05"})
	_, startUnix, _, _, _ = roadmapBar(t, after, issue.ID)
	assert.EqualValues(t, 1772668800, startUnix, "dragging the start edge left moves the bar left")

	// Drag the edge right: a later start.
	after = roadmapWrite(t, token, "/issues/5/dates",
		map[string]any{"repo": "user2/repo1", "start": "2026-03-20"})
	_, startUnix, _, _, _ = roadmapBar(t, after, issue.ID)
	assert.EqualValues(t, 1773964800, startUnix, "dragging the start edge right moves the bar right")
}

// getRoadmap reads the chart the same way every client does, so the assertions below are
// over the published shape rather than over an internal one.
func getRoadmap(t *testing.T, token, query string) roadmapPayload {
	t.Helper()
	req := NewRequest(t, "GET", planningv1.BasePath+"/roadmap?"+query).AddTokenAuth(token)
	var payload roadmapPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &payload)
	return payload
}

// rollupOf finds one rollup row on the chart.
func rollupOf(t *testing.T, payload roadmapPayload, kind, key string) int {
	t.Helper()
	for i, rollup := range payload.Rollups {
		if rollup.Kind == kind && rollup.Key == key {
			return i
		}
	}
	t.Fatalf("the chart has no %s rollup for %q", kind, key)
	return -1
}

// TestAPIPlanningRoadmapFlagsAParentThatEndsBeforeItsChildrenAtEveryZoom is the check the
// chart is the only place to see, and the filter it is most needed under.
//
// At zoom=parent no child bar is drawn at all, so a rollup folded from the drawn bars would
// contain a set of nothing and the warning could never fire. The rows come from their own
// fetch, so the same parent is flagged with no child on screen.
func TestAPIPlanningRoadmapFlagsAParentThatEndsBeforeItsChildrenAtEveryZoom(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	epicType := issueType(t, 1, "epic", "#8250df", "octicon-rocket", 1)
	setIssueType(t, token, "user2/repo1", 1, epicType.ID)
	storyType := issueType(t, 1, "story", "#2da44e", "octicon-tasklist", 3)
	setIssueType(t, token, "user2/repo1", 5, storyType.ID)
	setIssueParent(t, token, "user2/repo1", 5, 1)

	// The parent declares 2026-03-01 to 2026-03-11; the story filed under it runs to 2026-03-25.
	roadmapWrite(t, token, "/issues/1/dates",
		map[string]any{"repo": "user2/repo1", "start": "2026-03-01", "end": "2026-03-11"})
	roadmapWrite(t, token, "/issues/5/dates",
		map[string]any{"repo": "user2/repo1", "start": "2026-03-01", "end": "2026-03-25"})

	atIssue := getRoadmap(t, token, "repo_id=1&limit=200")
	row := atIssue.Rollups[rollupOf(t, atIssue, "parent", "1")]
	assert.Equal(t, 1, row.Children, "the parent issue is not one of its own children")
	assert.False(t, row.ContainsChildren)
	assert.EqualValues(t, 1773187200, row.DeclaredEndUnix, "2026-03-11, the parent's own deadline")
	assert.EqualValues(t, 1774396800, row.EndUnix, "2026-03-25, the story's")
	assert.Equal(t, `parent "issue1" (#1) ends 14 days before the work filed under it`, row.Warning)
	assert.Equal(t, "Move the parent's deadline to 2026-03-25, or move story #4 earlier.", row.SuggestedAction)
	assert.EqualValues(t, 1, row.IssueID, "the row names the parent issue, so a bracket can be opened")

	atParent := getRoadmap(t, token, "repo_id=1&limit=200&zoom=parent")
	assert.Empty(t, atParent.Bars, "a rolled-up zoom lists brackets, not the bars under them")
	assert.Equal(t, "parent", atParent.Zoom)
	same := atParent.Rollups[rollupOf(t, atParent, "parent", "1")]
	assert.Equal(t, row.Warning, same.Warning, "the same parent is flagged where no child is drawn")
	assert.Equal(t, row.SuggestedAction, same.SuggestedAction)
	assert.Equal(t, 1, same.Children)

	// The ruler follows what is drawn: three weeks and a bit is a week ruler.
	assert.Equal(t, "week", atParent.Ruler.Unit)
	require.NotEmpty(t, atParent.Ruler.Ticks)
	assert.Equal(t, "w/c 23 Feb", atParent.Ruler.Ticks[0].Label, "the axis starts on a unit boundary in UTC")

	// Moving the parent's deadline past its children clears the warning; it is a warning, not
	// a refusal, and it goes away when the schedule stops contradicting itself.
	roadmapWrite(t, token, "/issues/1/dates",
		map[string]any{"repo": "user2/repo1", "end": "2026-03-25"})
	fixed := getRoadmap(t, token, "repo_id=1&limit=200&zoom=parent")
	contained := fixed.Rollups[rollupOf(t, fixed, "parent", "1")]
	assert.True(t, contained.ContainsChildren)
	assert.Empty(t, contained.Warning)
}

// TestAPIPlanningRoadmapGroupMoveWritesWhatTheBoardsGroupMoveWrites is what makes the chart's
// vertical drag and the board's group move one operation: both go through PlanGroupMove, so
// both leave the same field on the issue.
func TestAPIPlanningRoadmapGroupMoveWritesWhatTheBoardsGroupMoveWrites(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	bug := issueType(t, 1, "bug", "#d1242f", "octicon-bug", 3)
	task := issueType(t, 1, "task", "#57606a", "octicon-checklist", 4)
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	setIssueType(t, token, "user2/repo1", 1, task.ID)

	after := roadmapWrite(t, token, "/issues/1/group",
		map[string]any{"repo": "user2/repo1", "group_by": "type", "group": "bug"})
	assert.Equal(t, "type", after.GroupBy)
	require.NotEmpty(t, after.Groups)
	assert.Equal(t, "bug", after.Groups[0].Key, "the chart answers with the group the bar landed in")

	assigned, err := planning_model.AssignmentsFor(t.Context(), []int64{1})
	require.NoError(t, err)
	assert.Equal(t, bug.ID, assigned[1], "the group IS the assignment, so the move rewrote it")

	// The same request against the board's own group endpoint reaches the same field, which
	// is the point of there being one definition rather than two.
	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/cards/1/group",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "group_by": "type", "group": "task"}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
	assigned, err = planning_model.AssignmentsFor(t.Context(), []int64{1})
	require.NoError(t, err)
	assert.Equal(t, task.ID, assigned[1], "a card never carries two assignments; the move replaces it")

	// Grouping off leaves no field to write, so the move is refused rather than guessed at.
	req = NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issues/1/group",
		map[string]any{"repo": "user2/repo1", "group_by": "none", "group": "bug"}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusBadRequest), &refusal)
	assert.Contains(t, refusal.Message, "not grouped")
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")
}

// TestAPIPlanningRoadmapRefusesAGroupMoveWithoutIssueWrite asserts the refusal AND that
// nothing was written: a 403 that had already written would be a worse defect than no guard.
// user4 can read user2/repo1, which is public, and cannot write its Issues unit.
func TestAPIPlanningRoadmapRefusesAGroupMoveWithoutIssueWrite(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	issueType(t, 1, "bug", "#d1242f", "octicon-bug", 3)
	task := issueType(t, 1, "task", "#57606a", "octicon-checklist", 4)
	ownerToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	setIssueType(t, ownerToken, "user2/repo1", 1, task.ID)
	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issues/1/group",
		map[string]any{"repo": "user2/repo1", "group_by": "type", "group": "bug"}).AddTokenAuth(outsiderToken)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusForbidden), &refusal)
	assert.Equal(t, "forbidden", refusal.Code)
	assert.Contains(t, refusal.Message, "Issues")
	assert.Contains(t, refusal.Message, "user2/repo1")
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")

	assigned, err := planning_model.AssignmentsFor(t.Context(), []int64{1})
	require.NoError(t, err)
	assert.Equal(t, task.ID, assigned[1], "the refused move left the group where it was")
}

// insertParentIssues creates one parent issue and its children directly, linked through
// plan_issue_parent, so their creation order is controllable: oldest-first paging is what
// decides which of them a short page holds. It returns the parent issue's own id.
func insertParentIssues(t *testing.T, firstID int64, childUnix []int64, parentUnix int64) int64 {
	t.Helper()
	rows := make([]*issues_model.Issue, 0, len(childUnix)+1)
	add := func(id, at int64) {
		rows = append(rows, &issues_model.Issue{
			ID: id, RepoID: 1, Index: id, PosterID: 2, Title: "paged",
			CreatedUnix: timeutil.TimeStamp(at), UpdatedUnix: timeutil.TimeStamp(at),
		})
	}
	for i, at := range childUnix {
		add(firstID+int64(i), at)
	}
	parentID := firstID + int64(len(childUnix))
	add(parentID, parentUnix)
	require.NoError(t, db.Insert(t.Context(), rows))

	links := make([]*planning_model.IssueParent, 0, len(childUnix))
	for i := range childUnix {
		links = append(links, &planning_model.IssueParent{ChildIssueID: firstID + int64(i), ParentIssueID: parentID})
	}
	require.NoError(t, db.Insert(t.Context(), links))
	return parentID
}

// TestAPIPlanningRoadmapMarksARollupPartialPastThePageLimit is the shipped defect the second
// fetch also fixes: a rollup over more children than one page holds is a floor, and printing
// a progress percentage over it would present a prefix as a measurement.
func TestAPIPlanningRoadmapMarksARollupPartialPastThePageLimit(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	const children = 205
	childUnix := make([]int64, children)
	for i := range childUnix {
		childUnix[i] = 1772323200
	}
	parentID := insertParentIssues(t, 9000, childUnix, 1772323200)

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	payload := getRoadmap(t, token, "repo_id=1&parent_issue_id="+strconv.FormatInt(parentID, 10)+"&limit=250")

	row := payload.Rollups[rollupOf(t, payload, "parent", strconv.FormatInt(parentID, 10))]
	assert.True(t, row.Partial, "the rollup hit its own cap")
	assert.Zero(t, row.Progress, "a fraction of an unknown denominator is not a measurement")
	assert.Equal(t, 200, row.Children)
}

// TestAPIPlanningRoadmapAtParentZoomPagesOverParentsNotOverIssues: at zoom=parent the fetch
// selects every issue that is itself a recorded parent, so a page of N holds N parents. Paging
// over every issue instead would drop a parent whose children all sit past the limit, and
// truncated would be about issues, not parents.
func TestAPIPlanningRoadmapAtParentZoomPagesOverParentsNotOverIssues(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// Four children created before their parent issue, so oldest-first fills a short page
	// with them alone; the second parent is created after all of them.
	early := insertParentIssues(t, 9100, []int64{1000, 1001, 1002, 1003}, 1004)
	late := insertParentIssues(t, 9200, []int64{2001}, 2000)

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	payload := getRoadmap(t, token, "repo_id=1&zoom=parent&limit=3")

	assert.Empty(t, payload.Bars, "a rolled-up zoom lists brackets, not the bars under them")
	assert.False(t, payload.Truncated, "two parents fit in a page of three")
	lateRow := payload.Rollups[rollupOf(t, payload, "parent", strconv.FormatInt(late, 10))]
	assert.Equal(t, 1, lateRow.Children, "the parent every issue-paged page would have missed")
	earlyRow := payload.Rollups[rollupOf(t, payload, "parent", strconv.FormatInt(early, 10))]
	assert.Equal(t, 4, earlyRow.Children, "its rollup still counts every child, page or no page")

	// parent_issue_id still narrows the chart to one parent's subtree at this zoom.
	one := getRoadmap(t, token, "repo_id=1&zoom=parent&limit=3&parent_issue_id="+strconv.FormatInt(late, 10))
	require.Len(t, one.Rollups, 1)
	assert.Equal(t, strconv.FormatInt(late, 10), one.Rollups[0].Key)
}

// TestAPIPlanningRoadmapAtMilestoneZoomPagesOverMilestonesNotOverIssues: milestones are not
// issues, so a page of N holds N milestone rows. Paging over issues instead drops a milestone
// whose issues all sit past the limit.
func TestAPIPlanningRoadmapAtMilestoneZoomPagesOverMilestonesNotOverIssues(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	filler := issueType(t, 1, "filler", "#112233", "octicon-issue-opened", 3)
	// Four managed issues on no milestone, created before everything else, so oldest-first
	// fills a short page of ISSUES with them alone.
	rows := make([]*issues_model.Issue, 0, 5)
	assignments := make([]*planning_model.IssueTypeAssignment, 0, 5)
	for i := range 4 {
		id := int64(9300 + i)
		rows = append(rows, &issues_model.Issue{
			ID: id, RepoID: 1, Index: id, PosterID: 2, Title: "filler",
			CreatedUnix: timeutil.TimeStamp(1000 + int64(i)), UpdatedUnix: timeutil.TimeStamp(1000 + int64(i)),
		})
		assignments = append(assignments, &planning_model.IssueTypeAssignment{IssueID: id, TypeID: filler.ID})
	}
	require.NoError(t, db.Insert(t.Context(), rows))
	require.NoError(t, db.Insert(t.Context(), assignments))

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	// The milestone is created last and holds one managed issue created last.
	roadmapWrite(t, token, "/milestones", map[string]any{"repo": "user2/repo1", "title": "Sprint 9"})
	sprint, err := issues_model.GetMilestoneByRepoIDANDName(t.Context(), 1, "Sprint 9")
	require.NoError(t, err)
	require.NoError(t, db.Insert(t.Context(), &issues_model.Issue{
		ID: 9400, RepoID: 1, Index: 9400, PosterID: 2, Title: "late", MilestoneID: sprint.ID,
		CreatedUnix: timeutil.TimeStamp(2_000_000_000), UpdatedUnix: timeutil.TimeStamp(2_000_000_000),
	}))
	require.NoError(t, db.Insert(t.Context(), &planning_model.IssueTypeAssignment{IssueID: 9400, TypeID: filler.ID}))

	milestones, err := unittest.GetXORMEngine().Where("repo_id = ?", 1).Count(new(issues_model.Milestone))
	require.NoError(t, err)
	payload := getRoadmap(t, token, "repo_id=1&zoom=milestone&limit="+strconv.FormatInt(milestones+1, 10))

	assert.Empty(t, payload.Bars, "a rolled-up zoom lists brackets, not the bars under them")
	assert.False(t, payload.Truncated, "every milestone fits the page, whatever the issue count is")
	row := payload.Rollups[rollupOf(t, payload, "milestone", strconv.FormatInt(sprint.ID, 10))]
	assert.Equal(t, 1, row.Children, "the milestone every issue-paged page would have missed")

	// The milestone filter still narrows the chart at this zoom, where the page is over
	// milestones rather than over the issues it would otherwise have narrowed.
	one := getRoadmap(t, token, "repo_id=1&zoom=milestone&limit=200&milestone_id="+strconv.FormatInt(sprint.ID, 10))
	require.Len(t, one.Rollups, 1)
	assert.Equal(t, strconv.FormatInt(sprint.ID, 10), one.Rollups[0].Key)
	assert.False(t, one.Truncated)
}

// TestAPIPlanningRoadmapAttachesAnArrowToTheBracketItsEndFallsIn: at a rolled-up zoom the
// bar an edge pointed at is not drawn, so the edge attaches to the bracket holding it rather
// than vanishing. The gate/sequence distinction survives the re-keying, and an edge with both
// ends in one bracket is dropped: it says nothing about the order of the brackets.
func TestAPIPlanningRoadmapAttachesAnArrowToTheBracketItsEndFallsIn(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	checkout := insertParentIssues(t, 9500, []int64{1000, 1001}, 1002)
	billing := insertParentIssues(t, 9600, []int64{2000}, 2001)
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
	payload := getRoadmap(t, token, "repo_id=1&zoom=parent&limit=200")

	require.Len(t, payload.Arrows, 1, "the edge inside one bracket is dropped, the one across two is kept")
	arrow := payload.Arrows[0]
	assert.Equal(t, "parent:"+strconv.FormatInt(checkout, 10), arrow.FromRollup, "the edge attaches to the bracket its end falls in")
	assert.Equal(t, "parent:"+strconv.FormatInt(billing, 10), arrow.ToRollup)
	assert.EqualValues(t, 9500, arrow.FromIssueID, "the child issues are still named")
	assert.EqualValues(t, 9600, arrow.ToIssueID)
	assert.Equal(t, "depends_on", arrow.Kind)
	assert.True(t, arrow.Enforced, "the forge itself refuses the close, whatever zoom it is read at")

	narrowed := getRoadmap(t, token, "repo_id=1&zoom=parent&limit=200&parent_issue_id="+strconv.FormatInt(billing, 10))
	require.Len(t, narrowed.Rollups, 1)
	assert.Empty(t, narrowed.Arrows, "an edge whose other end has no bracket on the page has nothing to attach to")

	// A sequencing hint between the same pair keeps its own kind: it is enforced by nothing,
	// and a chart that flattened the two would read a hint as a gate.
	billingChild := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 9600})
	billingChild.Content = "### Relations\n\nPredecessor #9501\n"
	_, err := db.GetEngine(t.Context()).ID(billingChild.ID).Cols("content").Update(billingChild)
	require.NoError(t, err)

	payload = getRoadmap(t, token, "repo_id=1&zoom=parent&limit=200")
	kinds := map[string]bool{}
	for _, a := range payload.Arrows {
		require.Equal(t, "parent:"+strconv.FormatInt(checkout, 10), a.FromRollup)
		require.Equal(t, "parent:"+strconv.FormatInt(billing, 10), a.ToRollup)
		kinds[a.Kind] = a.Enforced
	}
	enforced, hasGate := kinds["depends_on"]
	require.True(t, hasGate)
	assert.True(t, enforced)
	sequencing, hasHint := kinds["predecessor"]
	require.True(t, hasHint, "the rendered cross-reference still draws a sequencing hint between the brackets")
	assert.False(t, sequencing, "sequencing is enforced by nothing")
}

// TestAPIPlanningRoadmapAtMilestoneZoomNarrowsChildrenNotMilestones: state names the state of
// the ISSUES, here as at every other zoom. Filtering the milestones by their own open/closed
// flag instead would hide an open milestone holding finished work.
func TestAPIPlanningRoadmapAtMilestoneZoomNarrowsChildrenNotMilestones(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	filler := issueType(t, 1, "filler2", "#112233", "octicon-issue-opened", 3)
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	roadmapWrite(t, token, "/milestones", map[string]any{"repo": "user2/repo1", "title": "Sprint 9"})
	sprint, err := issues_model.GetMilestoneByRepoIDANDName(t.Context(), 1, "Sprint 9")
	require.NoError(t, err)
	require.False(t, sprint.IsClosed, "the milestone itself is open; only the issue under it is closed")

	require.NoError(t, db.Insert(t.Context(), &issues_model.Issue{
		ID: 9700, RepoID: 1, Index: 9700, PosterID: 2, Title: "finished", MilestoneID: sprint.ID,
		IsClosed: true, ClosedUnix: timeutil.TimeStamp(1_700_000_000),
		CreatedUnix: timeutil.TimeStamp(1_600_000_000), UpdatedUnix: timeutil.TimeStamp(1_700_000_000),
	}))
	require.NoError(t, db.Insert(t.Context(), &planning_model.IssueTypeAssignment{IssueID: 9700, TypeID: filler.ID}))

	key := strconv.FormatInt(sprint.ID, 10)
	closed := getRoadmap(t, token, "repo_id=1&zoom=milestone&limit=200&state=closed")
	row := closed.Rollups[rollupOf(t, closed, "milestone", key)]
	assert.Equal(t, 1, row.Children, "an open milestone holding closed work is listed at state=closed")
	assert.Equal(t, 100, row.Progress)

	// Its only issue is closed, so the same milestone has no open work and yields no row.
	open := getRoadmap(t, token, "repo_id=1&zoom=milestone&limit=200&state=open")
	for _, rollup := range open.Rollups {
		assert.NotEqual(t, key, rollup.Key, "a milestone whose children all fall outside the state is no row")
	}
}
