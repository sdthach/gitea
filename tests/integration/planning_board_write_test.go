// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	auth_model "gitea.dev/models/auth"
	issues_model "gitea.dev/models/issues"
	project_model "gitea.dev/models/project"
	"gitea.dev/models/unittest"
	api "gitea.dev/modules/structs"
	planningv1 "gitea.dev/routers/api/planning/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// columnCardOrder reads one column's cards in the order the board answers them, across
// whichever group they fall under — group_by=none puts everything in the one group.
func columnCardOrder(t *testing.T, board boardPayload, columnID int64) []int64 {
	t.Helper()
	for _, group := range board.Groups {
		for _, column := range group.Columns {
			if column.ColumnID == columnID {
				ids := make([]int64, 0, len(column.Cards))
				for _, card := range column.Cards {
					ids = append(ids, card.IssueID)
				}
				return ids
			}
		}
	}
	t.Fatalf("no column %d on the board", columnID)
	return nil
}

// TestPlanningBoardOrderColumnReordersEveryCardInOneCall covers the first write: issue2
// (fixture column 0, the legacy default) and issue1 (fixture column 1) both land in column 1,
// resorted in the order given, in the one call GitHub Projects' own board makes for a
// whole-column drag.
func TestPlanningBoardOrderColumnReordersEveryCardInOneCall(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/columns/1/order",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "issue_ids": "2,1", "group_by": "none"}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var board boardPayload
	DecodeJSON(t, resp, &board)

	assert.Equal(t, []int64{2, 1}, columnCardOrder(t, board, 1), "the write answers with the board as it now stands")

	row2 := unittest.AssertExistsAndLoadBean(t, &project_model.ProjectIssue{IssueID: 2, ProjectID: 1})
	assert.Equal(t, int64(1), row2.ProjectColumnID)
	assert.Equal(t, int64(0), row2.Sorting)
	row1 := unittest.AssertExistsAndLoadBean(t, &project_model.ProjectIssue{IssueID: 1, ProjectID: 1})
	assert.Equal(t, int64(1), row1.ProjectColumnID)
	assert.Equal(t, int64(1), row1.Sorting)
}

// TestPlanningBoardOrderColumnRefusesBadRequests covers every 422 the reorder answers other
// than the issue_ids count boundary, which needs its own test. Every case leaves issue1's row
// exactly where the fixtures put it — column 1, sorting 0.
//
// Column 1 is the project's default column, so it already holds both issue1 (its own row) and
// issue2 (the legacy project_board_id=0 row project/issue.go renders there) — every case below
// that targets column 1 without refusing before the completeness check names both.
func TestPlanningBoardOrderColumnRefusesBadRequests(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	for _, tc := range []struct {
		name   string
		column string
		body   map[string]any
		code   string
	}{
		{"empty", "1", map[string]any{"repo": "user2/repo1", "project_id": 1, "issue_ids": ""}, "bad_issue_ids"},
		{"non_numeric", "1", map[string]any{"repo": "user2/repo1", "project_id": 1, "issue_ids": "1,abc"}, "bad_issue_ids"},
		{"duplicate", "1", map[string]any{"repo": "user2/repo1", "project_id": 1, "issue_ids": "1,1"}, "bad_issue_ids"},
		// column 4 belongs to project 4, not project 1.
		{"column_not_in_project", "4", map[string]any{"repo": "user2/repo1", "project_id": 1, "issue_ids": "1"}, "column_not_in_project"},
		// leaves issue2 out of a column that holds it.
		{"incomplete_column", "1", map[string]any{"repo": "user2/repo1", "project_id": 1, "issue_ids": "1"}, "incomplete_column"},
		// names the whole column (1, 2) plus issue 7, which belongs to repo2.
		{"issue_not_in_repo", "1", map[string]any{"repo": "user2/repo1", "project_id": 1, "issue_ids": "1,2,7"}, "issue_not_in_repo"},
		// names the whole column (1, 2) plus issue 11, which belongs to repo1 but carries no
		// project_issue row for project 1.
		{"issue_not_in_project", "1", map[string]any{"repo": "user2/repo1", "project_id": 1, "issue_ids": "1,2,11"}, "issue_not_in_project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/columns/"+tc.column+"/order", tc.body).AddTokenAuth(token)
			resp := MakeRequest(t, req, http.StatusUnprocessableEntity)
			var refusal hubRefusal
			DecodeJSON(t, resp, &refusal)
			assert.Equal(t, tc.code, refusal.Code)
			assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")
		})
	}

	row := unittest.AssertExistsAndLoadBean(t, &project_model.ProjectIssue{IssueID: 1, ProjectID: 1})
	assert.Equal(t, int64(1), row.ProjectColumnID, "no refusal above moved the card")
	assert.Equal(t, int64(0), row.Sorting, "no refusal above wrote a new sorting")
	row2 := unittest.AssertExistsAndLoadBean(t, &project_model.ProjectIssue{IssueID: 2, ProjectID: 1})
	assert.Equal(t, int64(0), row2.ProjectColumnID, "no refusal above moved issue2 out of the legacy default row")
	assert.Equal(t, int64(0), row2.Sorting, "no refusal above wrote a new sorting")
}

// TestPlanningBoardOrderColumnRefusesUnknownGrouping proves group_by is validated before any
// write: sortings for both of column 1's cards are exactly where the fixtures put them.
func TestPlanningBoardOrderColumnRefusesUnknownGrouping(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/columns/1/order",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "issue_ids": "1,2", "group_by": "nonsense"}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusBadRequest)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "unknown_grouping", refusal.Code)

	row1 := unittest.AssertExistsAndLoadBean(t, &project_model.ProjectIssue{IssueID: 1, ProjectID: 1})
	assert.Equal(t, int64(1), row1.ProjectColumnID)
	assert.Equal(t, int64(0), row1.Sorting, "the bad grouping wrote no new sorting")
	row2 := unittest.AssertExistsAndLoadBean(t, &project_model.ProjectIssue{IssueID: 2, ProjectID: 1})
	assert.Equal(t, int64(0), row2.ProjectColumnID)
	assert.Equal(t, int64(0), row2.Sorting, "the bad grouping wrote no new sorting")
}

// TestPlanningBoardOrderColumnRefusesOverTheIssueIDsLimit is the 501st id tipping bad_issue_ids
// over the wire — caught before the first of them is even looked up, so the real id in the
// list is untouched. The boundary itself (500 accepted, 501 refused) is a parser-level unit
// test on parseIssueIDs; a real request this size is too slow to run here.
func TestPlanningBoardOrderColumnRefusesOverTheIssueIDsLimit(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	ids := make([]string, 501)
	ids[0] = "1"
	for i := 1; i < len(ids); i++ {
		ids[i] = strconv.Itoa(900000 + i)
	}

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/columns/1/order",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "issue_ids": strings.Join(ids, ",")}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusUnprocessableEntity)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "bad_issue_ids", refusal.Code)

	row := unittest.AssertExistsAndLoadBean(t, &project_model.ProjectIssue{IssueID: 1, ProjectID: 1})
	assert.Equal(t, int64(0), row.Sorting, "the call over the limit wrote nothing, including the one real id in it")
}

// TestPlanningBoardOrderColumnRefusesWithoutProjectsWrite: user4 can read user2/repo1 but
// writes neither of its units.
func TestPlanningBoardOrderColumnRefusesWithoutProjectsWrite(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/columns/1/order",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "issue_ids": "1"}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusForbidden)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "forbidden", refusal.Code)

	row := unittest.AssertExistsAndLoadBean(t, &project_model.ProjectIssue{IssueID: 1, ProjectID: 1})
	assert.Equal(t, int64(0), row.Sorting, "the refused reorder wrote nothing")
}

// TestPlanningBoardAddCardLandsInItsColumnAndGroup covers the second write: the created issue
// carries the title sent, sits in the column named, and its type is the group named — GitHub
// Projects' own "add item into a column and group" in one call.
func TestPlanningBoardAddCardLandsInItsColumnAndGroup(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	issueType(t, 1, "bug", "#d1242f", "octicon-bug", 3)
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	before := unittest.GetCount(t, &issues_model.Issue{RepoID: 1})

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/cards",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 2, "title": "Wire it up", "group_by": "type", "group": "bug"}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var board boardPayload
	DecodeJSON(t, resp, &board)

	unittest.AssertCount(t, &issues_model.Issue{RepoID: 1}, before+1)
	created := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{RepoID: 1, Title: "Wire it up"})
	assert.Equal(t, int64(2), columnOf(t, board, created.ID), "the write answers with the board as it now stands")
	assert.Equal(t, "bug", groupOf(t, board, created.ID))
}

// TestPlanningBoardAddCardWithTypeIDLandsUnderItsParent proves type_id is assigned before the
// group write: the new card, typed as it is created, ranks under issue1 once issue1 itself
// carries a type that outranks it.
func TestPlanningBoardAddCardWithTypeIDLandsUnderItsParent(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	epicType := issueType(t, 1, "epic", "#8250df", "octicon-milestone", 1)
	setIssueType(t, token, "user2/repo1", 1, epicType.ID)
	taskType := issueType(t, 1, "task", "#d1242f", "octicon-issue-opened", 3)
	before := unittest.GetCount(t, &issues_model.Issue{RepoID: 1})

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/cards",
		map[string]any{
			"repo": "user2/repo1", "project_id": 1, "column_id": 1, "title": "Child of the epic",
			"group_by": "parent", "group": "1", "type_id": taskType.ID,
		}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var board boardPayload
	DecodeJSON(t, resp, &board)

	unittest.AssertCount(t, &issues_model.Issue{RepoID: 1}, before+1)
	created := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{RepoID: 1, Title: "Child of the epic"})
	assert.Equal(t, "1", groupOf(t, board, created.ID), "the new card ranks under issue1, its parent group")
}

// TestPlanningBoardAddCardAcceptsATitleAtTheLimit is the title boundary's accepted side; the
// refused side (256) lives among TestPlanningBoardAddCardRefusesBadRequests' own cases.
func TestPlanningBoardAddCardAcceptsATitleAtTheLimit(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	title := strings.Repeat("x", 255)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/cards",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 1, "title": title}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)

	unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{RepoID: 1, Title: title})
}

// TestPlanningBoardAddCardRefusesBadRequests covers every 422 the create answers. A parent
// group with no type_id is refused untyped_issue once its target is confirmed to exist
// (parent_not_found otherwise) — the same shape a group move would answer once the card
// existed, resolved here before it does.
func TestPlanningBoardAddCardRefusesBadRequests(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	before := unittest.GetCount(t, &issues_model.Issue{RepoID: 1})

	longTitle := strings.Repeat("x", 256)
	for _, tc := range []struct {
		name string
		body map[string]any
		code string
	}{
		{"empty_title", map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 1, "title": "   "}, "bad_title"},
		{"title_too_long", map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 1, "title": longTitle}, "bad_title"},
		{"column_not_in_project", map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 4, "title": "x"}, "column_not_in_project"},
		{"type_not_visible", map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 1, "title": "x", "group_by": "type", "group": "nosuchtype"}, "type_not_visible"},
		{"assignee_not_found", map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 1, "title": "x", "group_by": "assignee", "group": "nosuchuser"}, "assignee_not_found"},
		{"parent_not_found", map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 1, "title": "x", "group_by": "parent", "group": "999999"}, "parent_not_found"},
		{"untyped_issue", map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 1, "title": "x", "group_by": "parent", "group": "1"}, "untyped_issue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/cards", tc.body).AddTokenAuth(token)
			resp := MakeRequest(t, req, http.StatusUnprocessableEntity)
			var refusal hubRefusal
			DecodeJSON(t, resp, &refusal)
			assert.Equal(t, tc.code, refusal.Code)
			assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")
		})
	}

	unittest.AssertCount(t, &issues_model.Issue{RepoID: 1}, before)
}

// TestPlanningBoardAddCardRefusesAGroupWithGroupingOff proves the early return in
// resolveAddCardGroup only covers an empty group: group_by absent (so grouping is none) with a
// non-empty group is refused exactly as moveBoardCardGroup refuses the same shape.
func TestPlanningBoardAddCardRefusesAGroupWithGroupingOff(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	before := unittest.GetCount(t, &issues_model.Issue{RepoID: 1})

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/cards",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 1, "title": "x", "group": "bug"}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusBadRequest)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Contains(t, refusal.Message, "not grouped")
	assert.NotEmpty(t, refusal.SuggestedAction)

	unittest.AssertCount(t, &issues_model.Issue{RepoID: 1}, before)
}

// TestPlanningBoardAddCardRefusedGroupCreatesNoIssue is "resolve the group first, then
// create"'s own proof: a group that cannot be applied leaves no row behind, named and checked
// on its own rather than folded into the table above's final count.
func TestPlanningBoardAddCardRefusedGroupCreatesNoIssue(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	before := unittest.GetCount(t, &issues_model.Issue{RepoID: 1})

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/cards",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 1, "title": "should not exist", "group_by": "type", "group": "nosuchtype"}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusUnprocessableEntity)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "type_not_visible", refusal.Code)

	unittest.AssertCount(t, &issues_model.Issue{RepoID: 1}, before)
	unittest.AssertNotExistsBean(t, &issues_model.Issue{RepoID: 1, Title: "should not exist"})
}

// TestPlanningBoardAddCardRefusesWithoutBothUnits: user4 can read user2/repo1 but writes
// neither of the two units the create needs.
func TestPlanningBoardAddCardRefusesWithoutBothUnits(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	before := unittest.GetCount(t, &issues_model.Issue{RepoID: 1})

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/cards",
		map[string]any{"repo": "user2/repo1", "project_id": 1, "column_id": 1, "title": "nope"}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusForbidden)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "forbidden", refusal.Code)

	unittest.AssertCount(t, &issues_model.Issue{RepoID: 1}, before)
}

// TestPlanningBoardAddCardRefusesProjectsWriteWithoutIssuesWrite covers the half of the
// forbidden matrix no fixture team carries: a team granted Projects write and nothing else, so
// its member can read and write the board but not create the issue a card add needs. No
// fixture team is scoped this narrowly (models/fixtures/team_unit.yml grants every unit the
// same access_mode per team), so this builds the org, repo and team through the API itself, as
// the instance admin, rather than reaching into the database directly; only the project and its
// default column, which the API path under test does not exercise, are inserted directly.
func TestPlanningBoardAddCardRefusesProjectsWriteWithoutIssuesWrite(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	admin := NewAPITestContext(t, "user1", "", auth_model.AccessTokenScopeWriteOrganization, auth_model.AccessTokenScopeWriteRepository)

	const orgName = "planningprojectsonlyorg"
	doAPICreateOrganization(admin, &api.CreateOrgOption{UserName: orgName, Visibility: "private"})(t)
	var repo api.Repository
	doAPICreateOrganizationRepository(admin, orgName, &api.CreateRepoOption{Name: "board", AutoInit: true},
		func(_ *testing.T, created api.Repository) { repo = created })(t)

	project := &project_model.Project{Title: "board", RepoID: repo.ID, Type: project_model.TypeRepository, TemplateType: project_model.TemplateTypeBasicKanban}
	require.NoError(t, project_model.NewProject(t.Context(), project))
	column := unittest.AssertExistsAndLoadBean(t, &project_model.Column{ProjectID: project.ID, Default: true})

	var team api.Team
	doAPICreateOrganizationTeam(admin, orgName, &api.CreateTeamOption{
		Name:       "board-only",
		UnitsMap:   map[string]string{"repo.projects": "write"},
		Visibility: "private",
	}, func(_ *testing.T, created api.Team) { team = created })(t)
	doAPIAddRepoToOrganizationTeam(admin, team.ID, orgName, "board")(t)
	doAPIAddUserToOrganizationTeam(admin, team.ID, "user4")(t)

	token := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	before := unittest.GetCount(t, &issues_model.Issue{RepoID: repo.ID})

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/board/cards",
		map[string]any{"repo": orgName + "/board", "project_id": project.ID, "column_id": column.ID, "title": "nope"}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusForbidden)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "forbidden", refusal.Code)

	unittest.AssertCount(t, &issues_model.Issue{RepoID: repo.ID}, before)
}
