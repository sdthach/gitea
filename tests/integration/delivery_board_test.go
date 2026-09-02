// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/timeutil"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	issue_service "gitea.dev/services/issue"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fork's integration tests run under Gitea's own harness (J8). Every name carries
// "Delivery", which is the one pattern `make delivery-integration` selects on.

// boardPayload is the shape GET /board answers with, reduced to what these tests assert.
type boardPayload struct {
	ProjectID int64  `json:"project_id"`
	GroupBy   string `json:"group_by"`
	Columns   []struct {
		ColumnID int64  `json:"column_id"`
		Title    string `json:"title"`
	} `json:"columns"`
	Lanes []struct {
		Key          string `json:"key"`
		Label        string `json:"label"`
		IsEmptyValue bool   `json:"is_empty_value"`
		Cards        int    `json:"cards"`
		Columns      []struct {
			ColumnID int64 `json:"column_id"`
			Title    string
			Cards    []struct {
				IssueID  int64 `json:"issue_id"`
				Number   int64 `json:"number"`
				ColumnID int64 `json:"column_id"`
			} `json:"cards"`
		} `json:"columns"`
	} `json:"lanes"`
	CanWrite     bool `json:"can_write"`
	CanEditIssue bool `json:"can_edit_issue"`
}

type deliveryRefusal struct {
	Code            string   `json:"code"`
	Message         string   `json:"message"`
	SuggestedAction string   `json:"suggested_action"`
	Accepted        []string `json:"accepted"`
}

// labelForLane creates a lane label in the repository, which is what ccpm's init.sh does
// before an epic sync. The board's lane move applies an existing label; it never invents one.
func labelForLane(t *testing.T, repoID int64, name string) *issues_model.Label {
	t.Helper()
	label := &issues_model.Label{RepoID: repoID, Name: name, Color: "#112233"}
	require.NoError(t, issues_model.NewLabel(t.Context(), label))
	return label
}

func laneOf(t *testing.T, board boardPayload, issueID int64) string {
	t.Helper()
	for _, lane := range board.Lanes {
		for _, column := range lane.Columns {
			for _, card := range column.Cards {
				if card.IssueID == issueID {
					return lane.Key
				}
			}
		}
	}
	t.Fatalf("issue %d is on no lane of the board", issueID)
	return ""
}

func columnOf(t *testing.T, board boardPayload, issueID int64) int64 {
	t.Helper()
	for _, lane := range board.Lanes {
		for _, column := range lane.Columns {
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

func getBoard(t *testing.T, token, query string) boardPayload {
	t.Helper()
	req := NewRequest(t, "GET", deliveryv1.BasePath+"/board?"+query).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var board boardPayload
	DecodeJSON(t, resp, &board)
	return board
}

// TestAPIDeliveryBoardRendersLanesOverGiteasColumns is O1: the columns are Gitea's own and
// the lanes are a rendering over rows it already returns. Nothing is stored to make a lane.
func TestAPIDeliveryBoardRendersLanesOverGiteasColumns(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	board := getBoard(t, token, "repo_id=1&project_id=1&group_by=none")
	assert.Equal(t, int64(1), board.ProjectID)
	assert.Equal(t, "none", board.GroupBy)
	require.Len(t, board.Columns, 3)
	assert.Equal(t, []string{"To Do", "In Progress", "Done"},
		[]string{board.Columns[0].Title, board.Columns[1].Title, board.Columns[2].Title})

	require.Len(t, board.Lanes, 1, "grouping off is Gitea's own board: one lane")
	assert.Equal(t, "All issues", board.Lanes[0].Label)
	assert.Positive(t, board.Lanes[0].Cards)
	require.Len(t, board.Lanes[0].Columns, 3, "the lane carries every column")

	// A legacy row with no column renders in the default one rather than vanishing.
	assert.Equal(t, board.Columns[0].ColumnID, columnOf(t, board, 2),
		"a card with no column lands in the default column, not off the board")
}

// TestAPIDeliveryBoardGroupsByTypeAssigneeAndEpic is SC 37's first half, over the wire.
func TestAPIDeliveryBoardGroupsByTypeAssigneeAndEpic(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	bug := labelForLane(t, 1, "type:bug")
	epic := labelForLane(t, 1, "epic:checkout")
	issue1 := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	require.NoError(t, issue_service.AddLabel(t.Context(), issue1, doer, bug))
	require.NoError(t, issue_service.AddLabel(t.Context(), issue1, doer, epic))

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	byType := getBoard(t, token, "repo_id=1&project_id=1&group_by=type")
	assert.Equal(t, "bug", laneOf(t, byType, 1))

	byEpic := getBoard(t, token, "repo_id=1&project_id=1&group_by=epic")
	assert.Equal(t, "checkout", laneOf(t, byEpic, 1))

	// issue 1 is assigned to user1 in the fixtures, so assignee grouping names a lane too.
	byAssignee := getBoard(t, token, "repo_id=1&project_id=1&group_by=assignee")
	assert.Equal(t, "user1", laneOf(t, byAssignee, 1))
}

// TestAPIDeliveryBoardKeepsAnUnsetValueInAnExplicitLane is O3/SC 37: nothing disappears from
// a board because a field is unset.
func TestAPIDeliveryBoardKeepsAnUnsetValueInAnExplicitLane(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	bug := labelForLane(t, 1, "type:bug")
	issue1 := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	require.NoError(t, issue_service.AddLabel(t.Context(), issue1, doer, bug))

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	ungrouped := getBoard(t, token, "repo_id=1&project_id=1&group_by=none")
	grouped := getBoard(t, token, "repo_id=1&project_id=1&group_by=type")

	total := 0
	for _, lane := range grouped.Lanes {
		total += lane.Cards
	}
	assert.Equal(t, ungrouped.Lanes[0].Cards, total, "grouping loses no card")

	last := grouped.Lanes[len(grouped.Lanes)-1]
	assert.True(t, last.IsEmptyValue, "the empty-value lane is explicit and sorts last")
	assert.Equal(t, "no type label", last.Label, "the lane says why it is empty")
	assert.Positive(t, last.Cards)
}

// TestAPIDeliveryBoardRefusesAnUnknownGrouping is I4: the rejection names the offender and
// lists what is accepted, rather than falling back to a board nobody asked for.
func TestAPIDeliveryBoardRefusesAnUnknownGrouping(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/board?repo_id=1&project_id=1&group_by=milestone").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusBadRequest)
	var refusal deliveryRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "unknown_grouping", refusal.Code)
	assert.Contains(t, refusal.Message, "milestone")
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action (A21)")
	assert.Contains(t, refusal.Accepted, "assignee")
}

// TestAPIDeliveryBoardPerformsExactlyTwoWrites is O4/SC 37: a card moved between columns and
// a card moved between lanes, the second rewriting the underlying label.
func TestAPIDeliveryBoardPerformsExactlyTwoWrites(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	labelForLane(t, 1, "type:bug")
	task := labelForLane(t, 1, "type:task")
	issue1 := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	require.NoError(t, issue_service.AddLabel(t.Context(), issue1, doer, task))

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	before := getBoard(t, token, "repo_id=1&project_id=1&group_by=type")
	assert.True(t, before.CanWrite)
	assert.True(t, before.CanEditIssue)
	assert.Equal(t, "task", laneOf(t, before, 1))
	assert.Equal(t, int64(1), columnOf(t, before, 1))

	// Write one: between columns.
	req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/board/cards/1/column",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 3, "group_by": "type"}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var moved boardPayload
	DecodeJSON(t, resp, &moved)
	assert.Equal(t, int64(3), columnOf(t, moved, 1), "the write answers with the board as it now stands")

	// Write two: between lanes, which edits the grouping field itself.
	req = NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/board/cards/1/lane",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "group_by": "type", "lane": "bug"}).AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusOK)
	DecodeJSON(t, resp, &moved)
	assert.Equal(t, "bug", laneOf(t, moved, 1))
	assert.Equal(t, int64(3), columnOf(t, moved, 1), "a lane move does not move the card between columns")

	// The label itself was rewritten: the lane is not stored anywhere else.
	reloaded := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	require.NoError(t, reloaded.LoadLabels(t.Context()))
	names := make([]string, 0, len(reloaded.Labels))
	for _, l := range reloaded.Labels {
		names = append(names, l.Name)
	}
	assert.Contains(t, names, "type:bug")
	assert.NotContains(t, names, "type:task", "a card never carries two labels of the same grouping")
}

// TestAPIDeliveryBoardRefusesALaneMoveWhenGroupingIsOff is O4/SC 37: with grouping off there
// is nothing to write, and the refusal says which write does still work.
func TestAPIDeliveryBoardRefusesALaneMoveWhenGroupingIsOff(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	for _, groupBy := range []string{"none", ""} {
		req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/board/cards/1/lane",
			map[string]any{"repo": "user2/repo1", "project_id": 1, "group_by": groupBy, "lane": "bug"}).AddTokenAuth(token)
		resp := MakeRequest(t, req, http.StatusBadRequest)
		var refusal deliveryRefusal
		DecodeJSON(t, resp, &refusal)
		assert.Contains(t, refusal.Message, "not grouped")
		assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action (A21)")
		assert.Contains(t, refusal.SuggestedAction, "COLUMNS")
	}
}

// TestDeliveryBoardDegradesWhileTheTimelineStillRenders is SC 38: against a repository whose
// Projects unit is absent the board states the reason, and the timeline renders from the same
// dataset — the two views are independently deliverable.
//
// repo4 carries the Issues unit and no Projects unit in the fixtures, which is exactly the
// runtime shape a build without the Projects API presents to this endpoint.
func TestDeliveryBoardDegradesWhileTheTimelineStillRenders(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 4})
	_, err := repo.GetUnit(t.Context(), 8 /* unit.TypeProjects */)
	require.Error(t, err, "the fixture repository has no Projects unit, which is what this test needs")

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/board?repo_id=4&project_id=3").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusNotFound)
	var refusal deliveryRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "projects_unavailable", refusal.Code)
	assert.Contains(t, refusal.Message, repo.FullName())
	assert.NotEmpty(t, refusal.SuggestedAction, "the degradation states what to do about it (A21)")
	assert.Contains(t, refusal.SuggestedAction, "/timeline",
		"the reason points at the view that still works")

	// Same repository, same moment: the timeline answers.
	req = NewRequest(t, "GET", deliveryv1.BasePath+"/timeline?repo_id=4").AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
}

// TestDeliveryBoardAndTimelinePagesAreClientsOfTheAPI is E18/I14 for the two new pages.
func TestDeliveryBoardAndTimelinePagesAreClientsOfTheAPI(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	for path, endpoint := range map[string]string{
		"/delivery/board":    "/board",
		"/delivery/timeline": "/timeline",
	} {
		req := NewRequest(t, "GET", path)
		MakeRequest(t, req, http.StatusSeeOther)

		session := loginUser(t, "user2")
		req = NewRequest(t, "GET", path)
		resp := session.MakeRequest(t, req, http.StatusOK)
		assert.Contains(t, resp.Body.String(), deliveryv1.BasePath+endpoint,
			"the page fetches its rows from the documented endpoint (E18, I14)")
	}
}

// timelinePayload is the shape GET /timeline answers with.
type timelinePayload struct {
	Bars []struct {
		IssueID     int64  `json:"issue_id"`
		MilestoneID int64  `json:"milestone_id"`
		Number      int64  `json:"number"`
		StartUnix   int64  `json:"start_unix"`
		EndUnix     int64  `json:"end_unix"`
		StartSource string `json:"start_source"`
		EndSource   string `json:"end_source"`
		EndInferred bool   `json:"end_inferred"`
	} `json:"bars"`
	Arrows []struct {
		FromIssueID int64  `json:"from_issue_id"`
		ToIssueID   int64  `json:"to_issue_id"`
		Kind        string `json:"kind"`
		Enforced    bool   `json:"enforced"`
	} `json:"arrows"`
	Spans []struct {
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
	} `json:"spans"`
	Rows []struct {
		MilestoneID int64  `json:"milestone_id"`
		Title       string `json:"title"`
	} `json:"rows"`
	GroupBy string `json:"group_by"`
	Zoom    string `json:"zoom"`
	Lanes   []struct {
		Key   string `json:"key"`
		Label string `json:"label"`
		Cards int    `json:"cards"`
	} `json:"lanes"`
	Ruler struct {
		Unit      string `json:"unit"`
		StartUnix int64  `json:"start_unix"`
		EndUnix   int64  `json:"end_unix"`
		Ticks     []struct {
			Unix  int64  `json:"unix"`
			Label string `json:"label"`
		} `json:"ticks"`
	} `json:"ruler"`
	CanWrite  bool `json:"can_write"`
	Truncated bool `json:"truncated"`
	Unmanaged []struct {
		Number          int64  `json:"number"`
		Reason          string `json:"reason"`
		SuggestedAction string `json:"suggested_action"`
	} `json:"unmanaged"`
}

// TestAPIDeliveryTimelineDrawsFromActualsAndLabelsEverySource is SC 39 over the wire: a task
// with a recorded start and a close time draws from actuals; one with neither draws from
// created plus estimate and is marked inferred; each bar names its start and end source.
func TestAPIDeliveryTimelineDrawsFromActualsAndLabelsEverySource(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	epic := labelForLane(t, 1, "epic:checkout")
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	// Issue 1 is managed, has a ccpm start marker on a comment, and is closed: actuals.
	issue1 := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	require.NoError(t, issue_service.AddLabel(t.Context(), issue1, doer, epic))
	require.NoError(t, db.Insert(t.Context(), &issues_model.Comment{
		Type: issues_model.CommentTypeComment, IssueID: issue1.ID, PosterID: doer.ID,
		Content:     "## Progress\n\n<!-- ccpm:started=2001-01-01T00:00:00Z -->",
		CreatedUnix: timeutil.TimeStamp(978307200),
	}))
	issue1.IsClosed = true
	issue1.ClosedUnix = timeutil.TimeStamp(1_000_000_000)
	_, err := db.GetEngine(t.Context()).ID(issue1.ID).Cols("is_closed", "closed_unix").Update(issue1)
	require.NoError(t, err)

	// Issue 5 is managed and states nothing: created plus estimate, inferred.
	issue5 := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 5})
	require.NoError(t, issue_service.AddLabel(t.Context(), issue5, doer, epic))

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/timeline?repo_id=1&epic=checkout&limit=200").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var payload timelinePayload
	DecodeJSON(t, resp, &payload)

	bars := map[int64]int{}
	for i, bar := range payload.Bars {
		bars[bar.IssueID] = i
		assert.NotEmpty(t, bar.StartSource, "every bar names its start source (O8)")
		assert.NotEmpty(t, bar.EndSource, "every bar names its end source (O8)")
	}
	require.Contains(t, bars, int64(1))
	require.Contains(t, bars, int64(5))

	actual := payload.Bars[bars[1]]
	assert.Equal(t, "ccpm_started", actual.StartSource)
	assert.Equal(t, int64(978307200), actual.StartUnix, "the marker's own time, not the comment's")
	assert.Equal(t, "closed", actual.EndSource)
	assert.False(t, actual.EndInferred, "a bar drawn from actuals is not marked inferred")

	guess := payload.Bars[bars[5]]
	assert.Equal(t, "issue_created", guess.StartSource)
	assert.Equal(t, "effort_estimate", guess.EndSource)
	assert.True(t, guess.EndInferred, "an inferred end is distinguishable from a recorded one (O8)")

	require.NotEmpty(t, payload.Spans)
	assert.Equal(t, "epic", payload.Spans[0].Kind)
	assert.Equal(t, "checkout", payload.Spans[0].Key)
}

// TestAPIDeliveryTimelineListsAnUnmanagedIssueWithItsReason is SC 39/O10: never a fabricated
// bar.
func TestAPIDeliveryTimelineListsAnUnmanagedIssueWithItsReason(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/timeline?repo_id=1&limit=200").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var payload timelinePayload
	DecodeJSON(t, resp, &payload)

	assert.Empty(t, payload.Bars, "no fixture issue carries an epic label, so nothing is drawn")
	require.NotEmpty(t, payload.Unmanaged, "the issues are listed rather than dropped")
	for _, item := range payload.Unmanaged {
		assert.Contains(t, item.Reason, "ccpm does not manage this issue")
		assert.NotEmpty(t, item.SuggestedAction, "every error carries a suggested next action (A21)")
	}
}

// TestAPIDeliveryTimelineDistinguishesAGateFromASequencingHint is O9/N9: they do not read
// the same on a schedule, so they are not the same arrow.
func TestAPIDeliveryTimelineDistinguishesAGateFromASequencingHint(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	epic := labelForLane(t, 1, "epic:checkout")
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	for _, id := range []int64{1, 4, 5} {
		issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: id})
		if issue.RepoID != 1 {
			continue
		}
		require.NoError(t, issue_service.AddLabel(t.Context(), issue, doer, epic))
	}

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

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/timeline?repo_id=1&epic=checkout&limit=200").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var payload timelinePayload
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

// TestAPIDeliveryBoardRefusesBothWritesWithoutPermission is E10/I13 for the only two
// operations in this slice that mutate a user's data.
//
// The happy-path test proves the writes HAPPEN; this proves they are REFUSED, which is the
// half a permission guard actually exists for. Both refusals are checked twice: the status
// the caller sees, and the card itself afterwards — a 403 that had already written would be
// a worse defect than no guard at all.
//
// user4 can read user2/repo1, which is public, and can write neither its Projects unit nor
// its Issues unit.
func TestAPIDeliveryBoardRefusesBothWritesWithoutPermission(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	labelForLane(t, 1, "type:bug")
	task := labelForLane(t, 1, "type:task")
	issue1 := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	require.NoError(t, issue_service.AddLabel(t.Context(), issue1, owner, task))

	ownerToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	before := getBoard(t, ownerToken, "repo_id=1&project_id=1&group_by=type")
	assert.Equal(t, int64(1), columnOf(t, before, 1))
	assert.Equal(t, "task", laneOf(t, before, 1))

	// The outsider can read the board — this is a permission answer about writing, not
	// about seeing.
	outsiderView := getBoard(t, outsiderToken, "repo_id=1&project_id=1&group_by=type")
	assert.False(t, outsiderView.CanWrite, "the board tells the outsider it offers no column move")
	assert.False(t, outsiderView.CanEditIssue, "and no lane move")

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
			name: "lane",
			path: "/board/cards/1/lane",
			body: map[string]any{"repo": "user2/repo1", "project_id": 1, "group_by": "type", "lane": "bug"},
			unit: "issues",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+tc.path, tc.body).AddTokenAuth(outsiderToken)
			resp := MakeRequest(t, req, http.StatusForbidden)
			var refusal deliveryRefusal
			DecodeJSON(t, resp, &refusal)
			assert.Equal(t, "forbidden", refusal.Code)
			assert.Contains(t, refusal.Message, tc.unit, "the refusal names the unit it wanted")
			assert.Contains(t, refusal.Message, "user2/repo1")
			assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action (A21)")
		})
	}

	// The card is where it was, in the column it was in and the lane it was in. A refusal
	// that had already written would pass the status assertions above and still be a defect.
	after := getBoard(t, ownerToken, "repo_id=1&project_id=1&group_by=type")
	assert.Equal(t, int64(1), columnOf(t, after, 1), "the refused column move wrote nothing")
	assert.Equal(t, "task", laneOf(t, after, 1), "the refused lane move wrote nothing")

	reloaded := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	require.NoError(t, reloaded.LoadLabels(t.Context()))
	names := make([]string, 0, len(reloaded.Labels))
	for _, l := range reloaded.Labels {
		names = append(names, l.Name)
	}
	assert.Contains(t, names, "type:task", "the issue's own field is untouched")
	assert.NotContains(t, names, "type:bug")
}

// TestAPIDeliveryBoardRefusesAReadOfARepositoryTheCallerCannotSee is the read-side half of
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
func TestAPIDeliveryBoardRefusesAReadOfARepositoryTheCallerCannotSee(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user8"), auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/board?repo_id=3&project_id=2").AddTokenAuth(outsiderToken)
	resp := MakeRequest(t, req, http.StatusNotFound)
	var refusal deliveryRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "repo_not_found", refusal.Code,
		"a private repository's existence is hidden rather than reported as forbidden")
	assert.NotContains(t, refusal.Message, "Projects",
		"the refusal discloses nothing about how the repository is configured")
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action (A21)")

	// The same board, to a member of the org, answers — so the refusal is about the caller
	// and not about the board being broken.
	memberToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	req = NewRequest(t, "GET", deliveryv1.BasePath+"/board?repo_id=3&project_id=2").AddTokenAuth(memberToken)
	MakeRequest(t, req, http.StatusOK)
}

// TestAPIDeliveryTimelineRefusesARepositoryTheCallerCannotRead is the timeline's own guard:
// the chart is scoped by Gitea's permission check on the Issues unit (E12, I13).
//
// It answers 403 where the board answers 404, because the timeline has no visibility
// pre-check of its own — the Issues unit read IS its visibility check.
func TestAPIDeliveryTimelineRefusesARepositoryTheCallerCannotRead(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user8"), auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/timeline?repo_id=3").AddTokenAuth(outsiderToken)
	resp := MakeRequest(t, req, http.StatusForbidden)
	var refusal deliveryRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "forbidden", refusal.Code)
	assert.Contains(t, refusal.Message, "Issues unit")
	assert.Contains(t, refusal.Message, "org3/repo3")
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action (A21)")

	// A member of the org reads the same chart.
	memberToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	req = NewRequest(t, "GET", deliveryv1.BasePath+"/timeline?repo_id=3").AddTokenAuth(memberToken)
	MakeRequest(t, req, http.StatusOK)
}

// TestAPIDeliveryBoardAndTimelineRefuseAMissingScope covers the two 400s every request can
// hit: a board belongs to a repository and a chart covers one, so neither renders without
// being told which.
func TestAPIDeliveryBoardAndTimelineRefuseAMissingScope(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	for _, tc := range []struct{ path, code string }{
		{"/board", "missing_repo_id"},
		{"/board?repo_id=1", "missing_project_id"},
		{"/timeline", "missing_repo_id"},
	} {
		req := NewRequest(t, "GET", deliveryv1.BasePath+tc.path).AddTokenAuth(token)
		resp := MakeRequest(t, req, http.StatusBadRequest)
		var refusal deliveryRefusal
		DecodeJSON(t, resp, &refusal)
		assert.Equal(t, tc.code, refusal.Code, "GET %s", tc.path)
		assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action (A21)")
	}

	// A repository id that resolves to nothing, and a board that belongs to another
	// repository, are both not-found rather than empty results.
	for _, tc := range []struct{ path, code string }{
		{"/board?repo_id=999999&project_id=1", "repo_not_found"},
		{"/timeline?repo_id=999999", "repo_not_found"},
		{"/board?repo_id=1&project_id=2", "board_not_found"},
		{"/board?repo_id=1&project_id=999999", "board_not_found"},
	} {
		req := NewRequest(t, "GET", deliveryv1.BasePath+tc.path).AddTokenAuth(token)
		resp := MakeRequest(t, req, http.StatusNotFound)
		var refusal deliveryRefusal
		DecodeJSON(t, resp, &refusal)
		assert.Equal(t, tc.code, refusal.Code, "GET %s", tc.path)
		assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action (A21)")
	}
}

// TestAPIDeliveryBoardWriteRefusesAMalformedRequest covers the write path's own 400s and its
// two 422s: a card that is not on the board, and a lane whose label does not exist.
func TestAPIDeliveryBoardWriteRefusesAMalformedRequest(t *testing.T) {
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
		{"/board/cards/1/lane", map[string]any{"repo": "user2/repo1", "project_id": 1, "group_by": "type", "lane": "nosuchtype"}, http.StatusUnprocessableEntity, "label_not_found"},
		{"/board/cards/1/lane", map[string]any{"repo": "user2/repo1", "project_id": 1, "group_by": "assignee", "lane": "nosuchuser"}, http.StatusUnprocessableEntity, "assignee_not_found"},
		{"/board/cards/1/lane", map[string]any{"repo": "user2/repo1", "project_id": 1, "group_by": "milestone", "lane": "x"}, http.StatusBadRequest, "unknown_grouping"},
	} {
		req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+tc.path, tc.body).AddTokenAuth(token)
		resp := MakeRequest(t, req, tc.status)
		var refusal deliveryRefusal
		DecodeJSON(t, resp, &refusal)
		assert.Equal(t, tc.code, refusal.Code, "POST %s with %v", tc.path, tc.body)
		assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action (A21)")
	}
}

// TestAPIDeliveryTimelineRefusesAnUnknownState is the last of the timeline's own refusals.
func TestAPIDeliveryTimelineRefusesAnUnknownState(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/timeline?repo_id=1&state=archived").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusBadRequest)
	var refusal deliveryRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "unknown_state", refusal.Code)
	assert.Contains(t, refusal.Message, "archived")
	assert.Contains(t, refusal.Accepted, "closed")
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action (A21)")
}

// TestDeliveryBoardDegradesWhenProjectsAreDisabledInstanceWide is the second, coarser form of
// the degradation of O5/SC 38: not one repository missing the unit, but the whole instance
// switching Projects off — which is what a rollback to a build without them looks like.
//
// The board states the reason and points at the view that still works; the timeline renders
// from the same dataset at the same moment. The two views are independently deliverable.
func TestDeliveryBoardDegradesWhenProjectsAreDisabledInstanceWide(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	// The board answers while Projects are enabled, so the change below is what moves it.
	req := NewRequest(t, "GET", deliveryv1.BasePath+"/board?repo_id=1&project_id=1").AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)

	before := unit.DisabledRepoUnitsGet()
	unit.DisabledRepoUnitsSet([]unit.Type{unit.TypeProjects})
	t.Cleanup(func() { unit.DisabledRepoUnitsSet(before) })

	req = NewRequest(t, "GET", deliveryv1.BasePath+"/board?repo_id=1&project_id=1").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusNotFound)
	var refusal deliveryRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "projects_unavailable", refusal.Code)
	assert.Contains(t, refusal.Message, "this instance disables the Projects unit")
	assert.NotEmpty(t, refusal.SuggestedAction, "the degradation states what to do about it (A21)")
	assert.Contains(t, refusal.SuggestedAction, "/timeline",
		"the reason points at the view that still works")

	// Both writes degrade the same way, and neither is reported as a permission problem.
	for _, path := range []string{"/board/cards/1/column", "/board/cards/1/lane"} {
		req = NewRequestWithJSON(t, "POST", deliveryv1.BasePath+path,
			map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 3, "group_by": "type", "lane": "bug"}).AddTokenAuth(token)
		resp = MakeRequest(t, req, http.StatusNotFound)
		DecodeJSON(t, resp, &refusal)
		assert.Equal(t, "projects_unavailable", refusal.Code, "POST %s", path)
	}

	// Same instant, same dataset: the timeline needs no Projects API and answers.
	req = NewRequest(t, "GET", deliveryv1.BasePath+"/timeline?repo_id=1").AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)

	req = NewRequest(t, "GET", "/delivery/timeline")
	loginUser(t, "user2").MakeRequest(t, req, http.StatusOK)
}
