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
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/timeutil"
	planningv1 "gitea.dev/routers/api/planning/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fork's integration tests run under Gitea's own harness. Every name carries
// "Planning" or "Deployments", the pattern `make hub-integration` selects on.

// boardCard is one card as GET /board answers it, reduced to what these tests assert.
type boardCard struct {
	IssueID         int64    `json:"issue_id"`
	Number          int64    `json:"number"`
	ColumnID        int64    `json:"column_id"`
	MilestoneID     int64    `json:"milestone_id"`
	TimeEstimate    int64    `json:"time_estimate"`
	TrackedSeconds  int64    `json:"tracked_seconds"`
	Labels          []string `json:"labels"`
	Assignees       []string `json:"assignees"`
	AssigneeAvatars []struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	} `json:"assignee_avatars"`
}

// boardPayload is the shape GET /board answers with, reduced to what these tests assert.
type boardPayload struct {
	ProjectID int64  `json:"project_id"`
	GroupBy   string `json:"group_by"`
	Columns   []struct {
		ColumnID int64  `json:"column_id"`
		Title    string `json:"title"`
	} `json:"columns"`
	Groups []struct {
		Key          string `json:"key"`
		Label        string `json:"label"`
		IsEmptyValue bool   `json:"is_empty_value"`
		Cards        int    `json:"cards"`
		Columns      []struct {
			ColumnID int64 `json:"column_id"`
			Title    string
			Cards    []boardCard `json:"cards"`
		} `json:"columns"`
	} `json:"groups"`
	Tree []struct {
		IssueID       int64 `json:"issue_id"`
		ParentIssueID int64 `json:"parent_issue_id"`
	} `json:"tree"`
	Labels []struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Color string `json:"color"`
	} `json:"labels"`
	CanWrite     bool `json:"can_write"`
	CanEditIssue bool `json:"can_edit_issue"`
}

// labelNames collects a projection's label names, for an ElementsMatch assertion that
// does not care about order.
func labelNames(labels []struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
},
) []string {
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return names
}

type hubRefusal struct {
	Code            string   `json:"code"`
	Message         string   `json:"message"`
	SuggestedAction string   `json:"suggested_action"`
	Accepted        []string `json:"accepted"`
}

func groupOf(t *testing.T, board boardPayload, issueID int64) string {
	t.Helper()
	for _, group := range board.Groups {
		for _, column := range group.Columns {
			for _, card := range column.Cards {
				if card.IssueID == issueID {
					return group.Key
				}
			}
		}
	}
	t.Fatalf("issue %d is on no group of the board", issueID)
	return ""
}

func cardOf(t *testing.T, board boardPayload, issueID int64) boardCard {
	t.Helper()
	for _, group := range board.Groups {
		for _, column := range group.Columns {
			for _, card := range column.Cards {
				if card.IssueID == issueID {
					return card
				}
			}
		}
	}
	t.Fatalf("issue %d is on no card of the board", issueID)
	return boardCard{}
}

func columnOf(t *testing.T, board boardPayload, issueID int64) int64 {
	t.Helper()
	for _, group := range board.Groups {
		for _, column := range group.Columns {
			for _, card := range column.Cards {
				if card.IssueID == issueID {
					return column.ColumnID
				}
			}
		}
	}
	t.Fatalf("issue %d is in no column of the board", issueID)
	return 0
}

// enableTimetracker turns on the Issues unit's own time tracker: repo1's fixture config
// carries no enable_time_tracker key at all, which unmarshals to off.
func enableTimetracker(t *testing.T, token, repo string) {
	t.Helper()
	req := NewRequestWithJSON(t, "PATCH", "/api/v1/repos/"+repo, map[string]any{
		"has_issues":       true,
		"internal_tracker": map[string]any{"enable_time_tracker": true},
	}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
}

func getBoard(t *testing.T, token, query string) boardPayload {
	t.Helper()
	req := NewRequest(t, "GET", planningv1.BasePath+"/board?"+query).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var board boardPayload
	DecodeJSON(t, resp, &board)
	return board
}

// TestAPIPlanningBoardRendersGroupsOverGiteasColumns: the columns are Gitea's own and
// the groups are a rendering over rows it already returns. Nothing is stored to make a group.
func TestAPIPlanningBoardRendersGroupsOverGiteasColumns(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)
	enableTimetracker(t, token, "user2/repo1")
	issue2 := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 2})
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	require.NoError(t, issues_model.ChangeIssueTimeEstimate(t.Context(), issue2, doer, 3600))

	board := getBoard(t, token, "repo_id=1&project_id=1&group_by=none")
	assert.Equal(t, int64(1), board.ProjectID)
	assert.Equal(t, "none", board.GroupBy)
	require.Len(t, board.Columns, 3)
	assert.Equal(t, []string{"To Do", "In Progress", "Done"},
		[]string{board.Columns[0].Title, board.Columns[1].Title, board.Columns[2].Title})

	require.Len(t, board.Groups, 1, "grouping off is Gitea's own board: one group")
	assert.Equal(t, "All issues", board.Groups[0].Label)
	assert.Positive(t, board.Groups[0].Cards)
	require.Len(t, board.Groups[0].Columns, 3, "the group carries every column")

	// A legacy row with no column renders in the default one rather than vanishing.
	assert.Equal(t, board.Columns[0].ColumnID, columnOf(t, board, 2),
		"a card with no column lands in the default column, not off the board")

	assert.ElementsMatch(t, []string{"label1", "label2"}, labelNames(board.Labels),
		"the board carries the repository's own labels")
	for _, group := range board.Groups {
		for _, column := range group.Columns {
			for _, card := range column.Cards {
				if card.IssueID == 2 {
					assert.Equal(t, int64(1), card.MilestoneID, "issue2's fixture milestone carries onto its card")
					assert.EqualValues(t, 3600, card.TimeEstimate, "the estimate carries onto the card with no per-row facets call")
					assert.EqualValues(t, 3682, card.TrackedSeconds, "tracked_time.yml's own three non-deleted rows on issue2, summed over both users")
				}
				if card.IssueID == 3 {
					// issue3 carries neither a label nor an assignee in the fixtures. A nil
					// slice and an empty one decode identically except for this: encoding/json
					// leaves a Go slice nil for a JSON null and non-nil for [], so NotNil here
					// is exactly the check that the wire value was [], not null.
					assert.NotNil(t, card.Labels, "a card with no labels publishes an empty array, never null")
					assert.NotNil(t, card.Assignees, "a card with no assignees publishes an empty array, never null")
				}
			}
		}
	}
}

// TestAPIPlanningBoardLabelsIncludeTheOwningOrganization: repo3 is owned by org3, so its
// board carries org3's own labels alongside the repository's own — repoLabels' organization
// branch, which repo1's user ownership never reaches.
func TestAPIPlanningBoardLabelsIncludeTheOwningOrganization(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	memberToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	board := getBoard(t, memberToken, "repo_id=3&project_id=2")
	names := labelNames(board.Labels)
	assert.Contains(t, names, "orglabel3", "org3's own label")
	assert.Contains(t, names, "repo3label1", "repo3's own label")
}

// TestAPIPlanningBoardGroupsByTypeAssigneeAndParent covers the three groupings over the wire.
// Issue 5 (feature, rank 2) is issue 1's (bug, rank 3) parent; both are on project 1's board.
func TestAPIPlanningBoardGroupsByTypeAssigneeAndParent(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	bug := issueType(t, 1, "bug", "#d1242f", "octicon-bug", 3)
	feature := issueType(t, 1, "feature", "#0969da", "octicon-light-bulb", 2)

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)
	setIssueType(t, token, "user2/repo1", 1, bug.ID)
	setIssueType(t, token, "user2/repo1", 5, feature.ID)
	setIssueParent(t, token, "user2/repo1", 1, 5)

	byType := getBoard(t, token, "repo_id=1&project_id=1&group_by=type")
	assert.Equal(t, "bug", groupOf(t, byType, 1))

	byParent := getBoard(t, token, "repo_id=1&project_id=1&group_by=parent")
	assert.Equal(t, "5", groupOf(t, byParent, 1), "grouped under its root ancestor's own row")
	assert.Equal(t, "5", groupOf(t, byParent, 5), "the root itself lands in its own row")

	// issue 1 is assigned to user1 in the fixtures, so assignee grouping names a group too.
	byAssignee := getBoard(t, token, "repo_id=1&project_id=1&group_by=assignee")
	assert.Equal(t, "user1", groupOf(t, byAssignee, 1))

	// issue 1's assignee_avatars carries the same login, resolved to a non-empty avatar url
	// through the user's own avatar link — never left for a client to derive from the login.
	card := cardOf(t, byAssignee, 1)
	require.Len(t, card.AssigneeAvatars, 1)
	assert.Equal(t, "user1", card.AssigneeAvatars[0].Login)
	assert.NotEmpty(t, card.AssigneeAvatars[0].AvatarURL, "the avatar url is resolved, not left blank")
}

// TestPlanningBoardLabelsAParentGroupWhoseRootIsNotOnTheBoard: a group's label comes from
// its root issue's own title, fetched even when that root is not one of the board's own cards.
func TestPlanningBoardLabelsAParentGroupWhoseRootIsNotOnTheBoard(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	epic := issueType(t, 1, "epic", "#8250df", "octicon-rocket", 1)
	bug := issueType(t, 1, "bug", "#d1242f", "octicon-bug", 3)

	// The root is in repo1 — readable, so its title resolves — but carries no project_issue
	// row, so it is not one of project 1's own cards.
	now := timeutil.TimeStamp(1700000000)
	root := &issues_model.Issue{
		ID: 9900, RepoID: 1, Index: 9900, PosterID: 2, Title: "root not on the board",
		CreatedUnix: now, UpdatedUnix: now,
	}
	require.NoError(t, db.Insert(t.Context(), root))
	require.NoError(t, planning_model.UpsertAssignment(t.Context(), root.ID, epic.ID))

	setIssueType(t, token, "user2/repo1", 1, bug.ID)
	setIssueParent(t, token, "user2/repo1", 1, root.ID)

	board := getBoard(t, token, "repo_id=1&project_id=1&group_by=parent")
	key := strconv.FormatInt(root.ID, 10)
	assert.Equal(t, key, groupOf(t, board, 1))
	found := false
	for _, group := range board.Groups {
		if group.Key == key {
			found = true
			assert.Equal(t, root.Title, group.Label, "the group's title comes from the root, even off the board")
		}
	}
	assert.True(t, found, "the group for the root's row exists")
}

// TestAPIPlanningBoardKeepsAnUnsetValueInAnExplicitGroup: nothing disappears from
// a board because a field is unset.
func TestAPIPlanningBoardKeepsAnUnsetValueInAnExplicitGroup(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	bug := issueType(t, 1, "bug", "#d1242f", "octicon-bug", 3)

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)
	setIssueType(t, token, "user2/repo1", 1, bug.ID)

	ungrouped := getBoard(t, token, "repo_id=1&project_id=1&group_by=none")
	grouped := getBoard(t, token, "repo_id=1&project_id=1&group_by=type")

	total := 0
	for _, group := range grouped.Groups {
		total += group.Cards
	}
	assert.Equal(t, ungrouped.Groups[0].Cards, total, "grouping loses no card")

	last := grouped.Groups[len(grouped.Groups)-1]
	assert.True(t, last.IsEmptyValue, "the empty-value group is explicit and sorts last")
	assert.Equal(t, "no type assigned", last.Label, "the group says why it is empty")
	assert.Positive(t, last.Cards)
}

// TestAPIPlanningBoardRefusesAnUnknownGrouping: the rejection names the offender and
// lists what is accepted, rather than falling back to a board nobody asked for.
func TestAPIPlanningBoardRefusesAnUnknownGrouping(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", planningv1.BasePath+"/board?repo_id=1&project_id=1&group_by=milestone").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusBadRequest)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "unknown_grouping", refusal.Code)
	assert.Contains(t, refusal.Message, "milestone")
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")
	assert.Contains(t, refusal.Accepted, "assignee")
}

// TestAPIPlanningBoardPerformsExactlyTwoWrites: a card moved between columns and
// a card moved between groups, the second rewriting the underlying label.
func TestAPIPlanningBoardPerformsExactlyTwoWrites(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	bug := issueType(t, 1, "bug", "#d1242f", "octicon-bug", 3)
	task := issueType(t, 1, "task", "#57606a", "octicon-checklist", 4)

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)
	setIssueType(t, token, "user2/repo1", 1, task.ID)

	before := getBoard(t, token, "repo_id=1&project_id=1&group_by=type")
	assert.True(t, before.CanWrite)
	assert.True(t, before.CanEditIssue)
	assert.Equal(t, "task", groupOf(t, before, 1))
	assert.Equal(t, int64(1), columnOf(t, before, 1))

	// Write one: between columns.
	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/cards/1/column",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 3, "group_by": "type"}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var moved boardPayload
	DecodeJSON(t, resp, &moved)
	assert.Equal(t, int64(3), columnOf(t, moved, 1), "the write answers with the board as it now stands")

	// Write two: between groups, which edits the grouping field itself.
	req = NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/cards/1/group",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "group_by": "type", "group": "bug"}).AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusOK)
	DecodeJSON(t, resp, &moved)
	assert.Equal(t, "bug", groupOf(t, moved, 1))
	assert.Equal(t, int64(3), columnOf(t, moved, 1), "a group move does not move the card between columns")

	// The assignment itself was rewritten: the group is not stored anywhere else.
	assigned, err := planning_model.AssignmentsFor(t.Context(), []int64{1})
	require.NoError(t, err)
	assert.Equal(t, bug.ID, assigned[1], "a card never carries two assignments of the same grouping")
}

// TestAPIPlanningBoardRefusesAGroupMoveWhenGroupingIsOff: with grouping off there
// is nothing to write, and the refusal says which write does still work.
func TestAPIPlanningBoardRefusesAGroupMoveWhenGroupingIsOff(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	for _, groupBy := range []string{"none", ""} {
		req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/cards/1/group",
			map[string]any{"repo": "user2/repo1", "project_id": 1, "group_by": groupBy, "group": "bug"}).AddTokenAuth(token)
		resp := MakeRequest(t, req, http.StatusBadRequest)
		var refusal hubRefusal
		DecodeJSON(t, resp, &refusal)
		assert.Contains(t, refusal.Message, "not grouped")
		assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")
		assert.Contains(t, refusal.SuggestedAction, "COLUMNS")
	}
}

// TestPlanningBoardDegradesWhileTheRoadmapStillRenders: against a repository whose
// Projects unit is absent the board states the reason, and the roadmap renders from the same
// dataset — the two views are independently deliverable.
//
// repo4 carries the Issues unit and no Projects unit in the fixtures, which is exactly the
// runtime shape a build without the Projects API presents to this endpoint.
func TestPlanningBoardDegradesWhileTheRoadmapStillRenders(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 4})
	_, err := repo.GetUnit(t.Context(), 8 /* unit.TypeProjects */)
	require.Error(t, err, "the fixture repository has no Projects unit, which is what this test needs")

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", planningv1.BasePath+"/board?repo_id=4&project_id=3").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusNotFound)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "projects_unavailable", refusal.Code)
	assert.Contains(t, refusal.Message, repo.FullName())
	assert.NotEmpty(t, refusal.SuggestedAction, "the degradation states what to do about it")
	assert.Contains(t, refusal.SuggestedAction, "/roadmap",
		"the reason points at the view that still works")

	// Same repository, same moment: the roadmap answers.
	req = NewRequest(t, "GET", planningv1.BasePath+"/roadmap?repo_id=4").AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
}

// roadmapPayload is the shape GET /roadmap answers with.
type roadmapPayload struct {
	Bars []struct {
		IssueID        int64  `json:"issue_id"`
		MilestoneID    int64  `json:"milestone_id"`
		Number         int64  `json:"number"`
		StartUnix      int64  `json:"start_unix"`
		EndUnix        int64  `json:"end_unix"`
		StartSource    string `json:"start_source"`
		EndSource      string `json:"end_source"`
		EndInferred    bool   `json:"end_inferred"`
		TypeID         int64  `json:"type_id"`
		Type           string `json:"type"`
		TimeEstimate   int64  `json:"time_estimate"`
		TrackedSeconds int64  `json:"tracked_seconds"`
	} `json:"bars"`
	Arrows []struct {
		FromIssueID int64  `json:"from_issue_id"`
		ToIssueID   int64  `json:"to_issue_id"`
		Kind        string `json:"kind"`
		Enforced    bool   `json:"enforced"`
		FromRollup  string `json:"from_rollup"`
		ToRollup    string `json:"to_rollup"`
	} `json:"arrows"`
	Rollups []struct {
		Kind             string `json:"kind"`
		Key              string `json:"key"`
		Progress         int    `json:"progress"`
		Children         int    `json:"children"`
		Partial          bool   `json:"partial"`
		IssueID          int64  `json:"issue_id"`
		StartUnix        int64  `json:"start_unix"`
		EndUnix          int64  `json:"end_unix"`
		DeclaredEndUnix  int64  `json:"declared_end_unix"`
		ContainsChildren bool   `json:"contains_children"`
		Warning          string `json:"warning"`
		SuggestedAction  string `json:"suggested_action"`
	} `json:"rollups"`
	Milestones []struct {
		MilestoneID int64  `json:"milestone_id"`
		Title       string `json:"title"`
		StartUnix   int64  `json:"start_unix"`
		EndUnix     int64  `json:"end_unix"`
	} `json:"milestones"`
	Tree []struct {
		IssueID       int64 `json:"issue_id"`
		ParentIssueID int64 `json:"parent_issue_id"`
	} `json:"tree"`
	GroupBy string `json:"group_by"`
	Zoom    string `json:"zoom"`
	Groups  []struct {
		Key   string `json:"key"`
		Label string `json:"label"`
		Cards int    `json:"cards"`
	} `json:"groups"`
	Ruler struct {
		Unit      string `json:"unit"`
		StartUnix int64  `json:"start_unix"`
		EndUnix   int64  `json:"end_unix"`
		Ticks     []struct {
			Unix  int64  `json:"unix"`
			Label string `json:"label"`
		} `json:"ticks"`
	} `json:"ruler"`
	Labels []struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Color string `json:"color"`
	} `json:"labels"`
	CanWrite  bool `json:"can_write"`
	Truncated bool `json:"truncated"`
	Unmanaged []struct {
		Number          int64    `json:"number"`
		Reason          string   `json:"reason"`
		SuggestedAction string   `json:"suggested_action"`
		Labels          []string `json:"labels"`
		Assignees       []string `json:"assignees"`
		Type            string   `json:"type"`
		TypeID          int64    `json:"type_id"`
		MilestoneID     int64    `json:"milestone_id"`
		IsClosed        bool     `json:"is_closed"`
	} `json:"unmanaged"`
}

// TestAPIPlanningRoadmapDrawsFromActualsAndLabelsEverySource, over the wire: a task
// with a recorded start and a close time draws from actuals; one with neither draws from
// created plus estimate and is marked inferred; each bar names its start and end source.
func TestAPIPlanningRoadmapDrawsFromActualsAndLabelsEverySource(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// Issue 1 is managed, has a recorded start, and is closed: actuals.
	manageIssue(t, 1)
	issue1 := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	require.NoError(t, planning_model.UpsertIssueStart(t.Context(), issue1.ID, 978307200))
	issue1.IsClosed = true
	issue1.ClosedUnix = timeutil.TimeStamp(1_000_000_000)
	_, err := db.GetEngine(t.Context()).ID(issue1.ID).Cols("is_closed", "closed_unix").Update(issue1)
	require.NoError(t, err)

	// Issue 5 is managed and states nothing: created plus estimate, inferred.
	manageIssue(t, 5)

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", planningv1.BasePath+"/roadmap?repo_id=1&limit=200").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var payload roadmapPayload
	DecodeJSON(t, resp, &payload)

	bars := map[int64]int{}
	for i, bar := range payload.Bars {
		bars[bar.IssueID] = i
		assert.NotEmpty(t, bar.StartSource, "every bar names its start source")
		assert.NotEmpty(t, bar.EndSource, "every bar names its end source")
	}
	require.Contains(t, bars, int64(1))
	require.Contains(t, bars, int64(5))

	actual := payload.Bars[bars[1]]
	assert.Equal(t, "schedule", actual.StartSource)
	assert.Equal(t, int64(978307200), actual.StartUnix, "the recorded schedule's own time")
	assert.Equal(t, "closed", actual.EndSource)
	assert.False(t, actual.EndInferred, "a bar drawn from actuals is not marked inferred")

	guess := payload.Bars[bars[5]]
	assert.Equal(t, "issue_created", guess.StartSource)
	assert.Equal(t, "effort_estimate", guess.EndSource)
	assert.True(t, guess.EndInferred, "an inferred end is distinguishable from a recorded one")
}

// TestAPIPlanningRoadmapListsAnUnmanagedIssueWithItsReason: never a fabricated bar.
func TestAPIPlanningRoadmapListsAnUnmanagedIssueWithItsReason(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", planningv1.BasePath+"/roadmap?repo_id=1&limit=200").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var payload roadmapPayload
	DecodeJSON(t, resp, &payload)

	assert.Empty(t, payload.Bars, "no fixture issue carries a type, a parent or a start, so nothing is drawn")
	require.NotEmpty(t, payload.Unmanaged, "the issues are listed rather than dropped")
	assert.ElementsMatch(t, []string{"label1", "label2"}, labelNames(payload.Labels),
		"the roadmap carries the repository's own labels, same shape as the board")

	found, foundClosed := false, false
	for _, item := range payload.Unmanaged {
		assert.Contains(t, item.Reason, "no type, no parent and no start date")
		assert.NotEmpty(t, item.SuggestedAction, "every error carries a suggested next action")
		if item.Number == 1 {
			found = true
			assert.Equal(t, []string{"label1"}, item.Labels, "an unmanaged row carries its own labels")
			assert.Equal(t, []string{"user1"}, item.Assignees, "and its assignees")
			assert.False(t, item.IsClosed)
		}
		// Issue 5 (index 4) is closed and otherwise unmanaged in the fixtures: its
		// unmanaged row must carry the issue's own state, not a hardcoded false.
		if item.Number == 4 {
			foundClosed = true
			assert.True(t, item.IsClosed, "issue5 is closed in the fixtures")
		}
	}
	assert.True(t, found, "issue 1 is unmanaged in these fixtures")
	assert.True(t, foundClosed, "issue5 is unmanaged and closed in these fixtures")
}

// TestPlanningRoadmapLabelsAParentGroupWhoseRootIsOffChart: a parent-grouped row's label
// comes from the root issue's own title even when parent_issue_id narrows the fetch to the
// root's descendants and the root itself is never drawn — the same gap readBoard's own
// ApplyRootTitles closes for the board.
func TestPlanningRoadmapLabelsAParentGroupWhoseRootIsOffChart(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	epic := issueType(t, 1, "epic", "#8250df", "octicon-rocket", 1)
	bug := issueType(t, 1, "bug", "#d1242f", "octicon-bug", 3)

	now := timeutil.TimeStamp(1700000000)
	root := &issues_model.Issue{
		ID: 9901, RepoID: 1, Index: 9901, PosterID: 2, Title: "roadmap root off chart",
		CreatedUnix: now, UpdatedUnix: now,
	}
	require.NoError(t, db.Insert(t.Context(), root))
	require.NoError(t, planning_model.UpsertAssignment(t.Context(), root.ID, epic.ID))

	setIssueType(t, token, "user2/repo1", 1, bug.ID)
	setIssueParent(t, token, "user2/repo1", 1, root.ID)

	// parent_issue_id narrows the fetch to root's own descendants — never root itself — so
	// root draws no bar despite being fully managed.
	req := NewRequest(t, "GET", planningv1.BasePath+"/roadmap?repo_id=1&group_by=parent&limit=200&parent_issue_id="+strconv.FormatInt(root.ID, 10)).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var payload roadmapPayload
	DecodeJSON(t, resp, &payload)

	key := strconv.FormatInt(root.ID, 10)
	found := false
	for _, group := range payload.Groups {
		if group.Key == key {
			found = true
			assert.Equal(t, root.Title, group.Label, "the group's title comes from the root even though it draws no bar")
		}
	}
	assert.True(t, found, "the group for the off-chart root exists")
}

// TestAPIPlanningRoadmapDistinguishesAGateFromASequencingHint: they do not read
// the same on a schedule, so they are not the same arrow.
func TestAPIPlanningRoadmapDistinguishesAGateFromASequencingHint(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	manageIssue(t, 1)
	manageIssue(t, 5)
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	// The enforced edge lives in issue_dependency: issue 1 blocks issue 5.
	require.NoError(t, db.Insert(t.Context(), &issues_model.IssueDependency{
		UserID: doer.ID, IssueID: 5, DependencyID: 1,
	}))
	// The sequencing edge lives in the rendered body, because that table has no type column.
	issue5 := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 5})
	issue5.Content = "### Relations\n\nPredecessor #1\n"
	_, err := db.GetEngine(t.Context()).ID(issue5.ID).Cols("content").Update(issue5)
	require.NoError(t, err)

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", planningv1.BasePath+"/roadmap?repo_id=1&limit=200").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var payload roadmapPayload
	DecodeJSON(t, resp, &payload)

	kinds := map[string]bool{}
	for _, arrow := range payload.Arrows {
		if arrow.FromIssueID == 1 && arrow.ToIssueID == 5 {
			kinds[arrow.Kind] = arrow.Enforced
		}
	}
	enforced, hasGate := kinds["depends_on"]
	require.True(t, hasGate, "the issue_dependency row draws a gate")
	assert.True(t, enforced, "the forge itself refuses the close")

	sequencing, hasSequence := kinds["predecessor"]
	require.True(t, hasSequence, "the rendered cross-reference draws a sequencing hint")
	assert.False(t, sequencing, "sequencing is enforced by nothing")
}

// TestAPIPlanningBoardRefusesBothWritesWithoutPermission covers the only two board
// operations that mutate a user's data.
//
// The happy-path test proves the writes HAPPEN; this proves they are REFUSED, which is the
// half a permission guard actually exists for. Both refusals are checked twice: the status
// the caller sees, and the card itself afterwards — a 403 that had already written would be
// a worse defect than no guard at all.
//
// user4 can read user2/repo1, which is public, and can write neither its Projects unit nor
// its Issues unit.
func TestAPIPlanningBoardRefusesBothWritesWithoutPermission(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	issueType(t, 1, "bug", "#d1242f", "octicon-bug", 3)
	task := issueType(t, 1, "task", "#57606a", "octicon-checklist", 4)

	ownerToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	setIssueType(t, ownerToken, "user2/repo1", 1, task.ID)

	before := getBoard(t, ownerToken, "repo_id=1&project_id=1&group_by=type")
	assert.Equal(t, int64(1), columnOf(t, before, 1))
	assert.Equal(t, "task", groupOf(t, before, 1))

	// The outsider can read the board — this is a permission answer about writing, not
	// about seeing.
	outsiderView := getBoard(t, outsiderToken, "repo_id=1&project_id=1&group_by=type")
	assert.False(t, outsiderView.CanWrite, "the board tells the outsider it offers no column move")
	assert.False(t, outsiderView.CanEditIssue, "and no group move")

	for _, tc := range []struct {
		name string
		path string
		body map[string]any
		unit string
	}{
		{
			name: "column",
			path: "/board/cards/1/column",
			body: map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 3, "group_by": "type"},
			unit: "projects",
		},
		{
			name: "group",
			path: "/board/cards/1/group",
			body: map[string]any{"repo": "user2/repo1", "project_id": 1, "group_by": "type", "group": "bug"},
			unit: "issues",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := NewRequestWithJSON(t, "POST", planningv1.BasePath+tc.path, tc.body).AddTokenAuth(outsiderToken)
			resp := MakeRequest(t, req, http.StatusForbidden)
			var refusal hubRefusal
			DecodeJSON(t, resp, &refusal)
			assert.Equal(t, "forbidden", refusal.Code)
			assert.Contains(t, refusal.Message, tc.unit, "the refusal names the unit it wanted")
			assert.Contains(t, refusal.Message, "user2/repo1")
			assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")
		})
	}

	// The card is where it was, in the column it was in and the group it was in. A refusal
	// that had already written would pass the status assertions above and still be a defect.
	after := getBoard(t, ownerToken, "repo_id=1&project_id=1&group_by=type")
	assert.Equal(t, int64(1), columnOf(t, after, 1), "the refused column move wrote nothing")
	assert.Equal(t, "task", groupOf(t, after, 1), "the refused group move wrote nothing")

	assigned, err := planning_model.AssignmentsFor(t.Context(), []int64{1})
	require.NoError(t, err)
	assert.Equal(t, task.ID, assigned[1], "the issue's own field is untouched")
}

// TestAPIPlanningBoardRefusesAReadOfARepositoryTheCallerCannotSee is the read-side half of
// the board's guard, and it is the branch of boardReadable that a request can actually reach.
//
// user8 belongs to no team of org3 and is no collaborator on its private repo3, so it has no
// unit access at all. The board answers 404 rather than 403: a private repository's existence
// is hidden, not reported as forbidden. The answer also comes BEFORE boardAvailable, so the
// caller is never told how the repository's Projects unit is configured.
//
// The other branch of boardReadable — visible repository, unreadable Projects unit — is a
// fail-closed backstop no request reaches on this tree. Measured: team.go:214 raises every
// unit to the team's own AccessMode, and Permission.UnitAccessMode falls back to AccessMode
// for any unit the repository has, so a caller past HasAnyUnitAccess can always read a
// Projects unit that exists. It is asserted here as far as it can be, and reported as a
// backstop rather than as covered.
func TestAPIPlanningBoardRefusesAReadOfARepositoryTheCallerCannotSee(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user8"), auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", planningv1.BasePath+"/board?repo_id=3&project_id=2").AddTokenAuth(outsiderToken)
	resp := MakeRequest(t, req, http.StatusNotFound)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "repo_not_found", refusal.Code,
		"a private repository's existence is hidden rather than reported as forbidden")
	assert.NotContains(t, refusal.Message, "Projects",
		"the refusal discloses nothing about how the repository is configured")
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")

	// The same board, to a member of the org, answers — so the refusal is about the caller
	// and not about the board being broken.
	memberToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	req = NewRequest(t, "GET", planningv1.BasePath+"/board?repo_id=3&project_id=2").AddTokenAuth(memberToken)
	MakeRequest(t, req, http.StatusOK)
}

// TestAPIPlanningRoadmapRefusesARepositoryTheCallerCannotRead is the roadmap's own guard:
// the chart is scoped by Gitea's permission check on the Issues unit.
//
// It answers 403 where the board answers 404, because the roadmap has no visibility
// pre-check of its own — the Issues unit read IS its visibility check.
func TestAPIPlanningRoadmapRefusesARepositoryTheCallerCannotRead(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user8"), auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", planningv1.BasePath+"/roadmap?repo_id=3").AddTokenAuth(outsiderToken)
	resp := MakeRequest(t, req, http.StatusForbidden)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "forbidden", refusal.Code)
	assert.Contains(t, refusal.Message, "Issues unit")
	assert.Contains(t, refusal.Message, "org3/repo3")
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")

	// A member of the org reads the same chart.
	memberToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	req = NewRequest(t, "GET", planningv1.BasePath+"/roadmap?repo_id=3").AddTokenAuth(memberToken)
	MakeRequest(t, req, http.StatusOK)
}

// TestAPIPlanningBoardAndRoadmapRefuseAMissingScope covers the two 400s every request can
// hit: a board belongs to a repository and a chart covers one, so neither renders without
// being told which.
func TestAPIPlanningBoardAndRoadmapRefuseAMissingScope(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	for _, tc := range []struct{ path, code string }{
		{"/board", "missing_repo_id"},
		{"/board?repo_id=1", "missing_project_id"},
		{"/roadmap", "missing_repo_id"},
	} {
		req := NewRequest(t, "GET", planningv1.BasePath+tc.path).AddTokenAuth(token)
		resp := MakeRequest(t, req, http.StatusBadRequest)
		var refusal hubRefusal
		DecodeJSON(t, resp, &refusal)
		assert.Equal(t, tc.code, refusal.Code, "GET %s", tc.path)
		assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")
	}

	// A repository id that resolves to nothing, and a board that belongs to another
	// repository, are both not-found rather than empty results.
	for _, tc := range []struct{ path, code string }{
		{"/board?repo_id=999999&project_id=1", "repo_not_found"},
		{"/roadmap?repo_id=999999", "repo_not_found"},
		{"/board?repo_id=1&project_id=2", "board_not_found"},
		{"/board?repo_id=1&project_id=999999", "board_not_found"},
	} {
		req := NewRequest(t, "GET", planningv1.BasePath+tc.path).AddTokenAuth(token)
		resp := MakeRequest(t, req, http.StatusNotFound)
		var refusal hubRefusal
		DecodeJSON(t, resp, &refusal)
		assert.Equal(t, tc.code, refusal.Code, "GET %s", tc.path)
		assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")
	}
}

// TestAPIPlanningBoardWriteRefusesAMalformedRequest covers the write path's own 400s and its
// two 422s: a card that is not on the board, and a group whose label does not exist.
func TestAPIPlanningBoardWriteRefusesAMalformedRequest(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	for _, tc := range []struct {
		path   string
		body   any
		status int
		code   string
	}{
		{"/board/cards/1/column", "not an object", http.StatusBadRequest, "malformed_body"},
		{"/board/cards/1/column", map[string]any{"repo": "user2repo1", "project_id": 1, "column_id": 3}, http.StatusBadRequest, "bad_repo"},
		{"/board/cards/1/column", map[string]any{"repo": "user2/nope", "project_id": 1, "column_id": 3}, http.StatusNotFound, "repo_not_found"},
		{"/board/cards/1/column", map[string]any{"repo": "user2/repo1", "project_id": 999999, "column_id": 3}, http.StatusNotFound, "board_not_found"},
		{"/board/cards/999999/column", map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 3}, http.StatusNotFound, "card_not_found"},
		{"/board/cards/1/column", map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 999999}, http.StatusUnprocessableEntity, "column_not_found"},
		{"/board/cards/1/group", map[string]any{"repo": "user2/repo1", "project_id": 1, "group_by": "type", "group": "nosuchtype"}, http.StatusUnprocessableEntity, "type_not_visible"},
		{"/board/cards/1/group", map[string]any{"repo": "user2/repo1", "project_id": 1, "group_by": "assignee", "group": "nosuchuser"}, http.StatusUnprocessableEntity, "assignee_not_found"},
		{"/board/cards/1/group", map[string]any{"repo": "user2/repo1", "project_id": 1, "group_by": "milestone", "group": "x"}, http.StatusBadRequest, "unknown_grouping"},
	} {
		req := NewRequestWithJSON(t, "POST", planningv1.BasePath+tc.path, tc.body).AddTokenAuth(token)
		resp := MakeRequest(t, req, tc.status)
		var refusal hubRefusal
		DecodeJSON(t, resp, &refusal)
		assert.Equal(t, tc.code, refusal.Code, "POST %s with %v", tc.path, tc.body)
		assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")
	}
}

// TestAPIPlanningRoadmapRefusesAnUnknownState is the last of the roadmap's own refusals.
func TestAPIPlanningRoadmapRefusesAnUnknownState(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", planningv1.BasePath+"/roadmap?repo_id=1&state=archived").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusBadRequest)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "unknown_state", refusal.Code)
	assert.Contains(t, refusal.Message, "archived")
	assert.Contains(t, refusal.Accepted, "closed")
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")
}

// TestPlanningBoardDegradesWhenProjectsAreDisabledInstanceWide is the second, coarser form of
// the degradation: not one repository missing the unit, but the whole instance
// switching Projects off — which is what a rollback to a build without them looks like.
//
// The board states the reason and points at the view that still works; the roadmap renders
// from the same dataset at the same moment. The two views are independently deliverable.
func TestPlanningBoardDegradesWhenProjectsAreDisabledInstanceWide(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	// The board answers while Projects are enabled, so the change below is what moves it.
	req := NewRequest(t, "GET", planningv1.BasePath+"/board?repo_id=1&project_id=1").AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)

	before := unit.DisabledRepoUnitsGet()
	unit.DisabledRepoUnitsSet([]unit.Type{unit.TypeProjects})
	t.Cleanup(func() { unit.DisabledRepoUnitsSet(before) })

	req = NewRequest(t, "GET", planningv1.BasePath+"/board?repo_id=1&project_id=1").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusNotFound)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "projects_unavailable", refusal.Code)
	assert.Contains(t, refusal.Message, "this instance disables the Projects unit")
	assert.NotEmpty(t, refusal.SuggestedAction, "the degradation states what to do about it")
	assert.Contains(t, refusal.SuggestedAction, "/roadmap",
		"the reason points at the view that still works")

	// Both writes degrade the same way, and neither is reported as a permission problem.
	for _, path := range []string{"/board/cards/1/column", "/board/cards/1/group"} {
		req = NewRequestWithJSON(t, "POST", planningv1.BasePath+path,
			map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 3, "group_by": "type", "group": "bug"}).AddTokenAuth(token)
		resp = MakeRequest(t, req, http.StatusNotFound)
		DecodeJSON(t, resp, &refusal)
		assert.Equal(t, "projects_unavailable", refusal.Code, "POST %s", path)
	}

	// Same instant, same dataset: the roadmap needs no Projects API and answers.
	req = NewRequest(t, "GET", planningv1.BasePath+"/roadmap?repo_id=1").AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
}
