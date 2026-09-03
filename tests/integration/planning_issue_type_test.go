// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	auth_model "gitea.dev/models/auth"
	planning_model "gitea.dev/models/planning"
	"gitea.dev/models/unittest"
	planningv1 "gitea.dev/routers/api/planning/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// issueTypeRowPayload is the shape the issue-type CRUD endpoints answer with.
type issueTypeRowPayload struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Color   string `json:"color"`
	Icon    string `json:"icon"`
	Rank    int    `json:"rank"`
	Scope   string `json:"scope"`
	ScopeID int64  `json:"scope_id"`
}

// TestPlanningIssueTypeCRUDAsRepoAdmin covers create, update and delete as the repository
// admin, each reply carrying the row it changed.
func TestPlanningIssueTypeCRUDAsRepoAdmin(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issue-types",
		map[string]any{"repo_id": 1, "name": "Bug", "color": "#d1242f", "icon": "octicon-bug", "rank": 3}).AddTokenAuth(token)
	var created issueTypeRowPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &created)
	assert.Equal(t, "bug", created.Name, "stored lower-cased regardless of how it was sent")
	assert.Equal(t, "repo", created.Scope)
	assert.EqualValues(t, 1, created.ScopeID)
	unittest.AssertExistsAndLoadBean(t, &planning_model.IssueType{ID: created.ID, RepoID: 1, Name: "bug"})

	req = NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issue-types/"+strconv.FormatInt(created.ID, 10),
		map[string]any{"name": "bugfix", "color": "#ff0000", "icon": "octicon-bug", "rank": 2}).AddTokenAuth(token)
	var updated issueTypeRowPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &updated)
	assert.Equal(t, "bugfix", updated.Name)
	assert.Equal(t, "#ff0000", updated.Color)
	assert.Equal(t, 2, updated.Rank)

	req = NewRequest(t, "DELETE", planningv1.BasePath+"/issue-types/"+strconv.FormatInt(created.ID, 10)).AddTokenAuth(token)
	var deleted issueTypeRowPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &deleted)
	assert.Equal(t, "bugfix", deleted.Name, "the reply names the row as it stood just before deletion")
	unittest.AssertNotExistsBean(t, &planning_model.IssueType{ID: created.ID})
}

// TestPlanningIssueTypeCreateRefusedForAReader is the write's authorization check: user4 can
// read user2/repo1, which is public, and does not administer it. Nothing is written.
func TestPlanningIssueTypeCreateRefusedForAReader(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	readerToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issue-types",
		map[string]any{"repo_id": 1, "name": "bug", "color": "#d1242f", "icon": "octicon-bug", "rank": 3}).AddTokenAuth(readerToken)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusForbidden), &refusal)
	assert.Equal(t, "forbidden", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")

	unittest.AssertNotExistsBean(t, &planning_model.IssueType{RepoID: 1, Name: "bug"})
}

// TestPlanningIssueTypeDeleteRefusesInUseThenForceClearsBoth covers type_in_use, carrying the
// assignment count, and force=true deleting the type and its assignment together.
func TestPlanningIssueTypeDeleteRefusesInUseThenForceClearsBoth(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	bug := issueType(t, 1, "bug", "#d1242f", "octicon-bug", 3)
	setIssueType(t, token, "user2/repo1", 1, bug.ID)

	req := NewRequest(t, "DELETE", planningv1.BasePath+"/issue-types/"+strconv.FormatInt(bug.ID, 10)).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusConflict), &refusal)
	assert.Equal(t, "type_in_use", refusal.Code)
	assert.Contains(t, refusal.Message, "1")
	assert.Contains(t, refusal.SuggestedAction, "force")
	unittest.AssertExistsAndLoadBean(t, &planning_model.IssueType{ID: bug.ID})

	req = NewRequestWithJSON(t, "DELETE", planningv1.BasePath+"/issue-types/"+strconv.FormatInt(bug.ID, 10),
		map[string]any{"force": true}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
	unittest.AssertNotExistsBean(t, &planning_model.IssueType{ID: bug.ID})
	unittest.AssertNotExistsBean(t, &planning_model.IssueTypeAssignment{IssueID: 1, TypeID: bug.ID})
}

// TestPlanningIssueTypeSetShowsOnBoardGroupAndRoadmapBar: the same assignment the board groups
// by is what the roadmap's bar carries as type_id.
func TestPlanningIssueTypeSetShowsOnBoardGroupAndRoadmapBar(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	bug := issueType(t, 1, "bug", "#d1242f", "octicon-bug", 3)
	setIssueType(t, token, "user2/repo1", 1, bug.ID)

	board := getBoard(t, token, "repo_id=1&project_id=1&group_by=type")
	assert.Equal(t, "bug", groupOf(t, board, 1))

	manageIssue(t, 1, "checkout")
	roadmap := getRoadmap(t, token, "repo_id=1&limit=200")
	found := false
	for _, bar := range roadmap.Bars {
		if bar.IssueID == 1 {
			found = true
			assert.Equal(t, bug.ID, bar.TypeID)
			assert.Equal(t, "bug", bar.Type)
		}
	}
	assert.True(t, found, "issue 1 has a bar")
}

// TestPlanningIssueTypeAssignmentBatchReturnsRenderedIconSVG covers the batch read: it
// resolves each issue's assigned type and renders its icon, so a client needs no icon
// registry of its own.
func TestPlanningIssueTypeAssignmentBatchReturnsRenderedIconSVG(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	bug := issueType(t, 1, "bug", "#d1242f", "octicon-bug", 3)
	setIssueType(t, token, "user2/repo1", 1, bug.ID)

	req := NewRequest(t, "GET", planningv1.BasePath+"/issue-type-assignments?repo_id=1&issue_ids=1").AddTokenAuth(token)
	var rows []struct {
		IssueID int64  `json:"issue_id"`
		TypeID  int64  `json:"type_id"`
		Name    string `json:"name"`
		IconSVG string `json:"icon_svg"`
	}
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &rows)
	require.Len(t, rows, 1)
	assert.EqualValues(t, 1, rows[0].IssueID)
	assert.Equal(t, "bug", rows[0].Name)
	assert.NotEmpty(t, rows[0].IconSVG)
	assert.True(t, strings.HasPrefix(rows[0].IconSVG, "<svg"), "a real icon renders as an svg element")
}

// TestPlanningIssueTypeAssignmentBatchRefusesTooManyIDs: more than 200 ids is refused rather
// than silently truncated.
func TestPlanningIssueTypeAssignmentBatchRefusesTooManyIDs(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	ids := make([]string, 0, 201)
	for i := 1; i <= 201; i++ {
		ids = append(ids, strconv.Itoa(i))
	}
	req := NewRequest(t, "GET", planningv1.BasePath+"/issue-type-assignments?repo_id=1&issue_ids="+strings.Join(ids, ",")).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "too_many_ids", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")
}

// TestPlanningIssueTypeAssignmentsRequireRepoIDAndScopeToIt: repo_id is required, and an id
// naming an issue outside that repository is dropped rather than leaking its assignment.
func TestPlanningIssueTypeAssignmentsRequireRepoIDAndScopeToIt(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	bug := issueType(t, 2, "bug", "#d1242f", "octicon-bug", 3) // scoped to repo2
	require.NoError(t, planning_model.UpsertAssignment(t.Context(), 4, bug.ID))

	readerToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", planningv1.BasePath+"/issue-type-assignments?issue_ids=4").AddTokenAuth(readerToken)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "missing_repo_id", refusal.Code)

	// Issue 4 lives in repo2; asking with repo_id=1 returns nothing for it rather than erroring.
	req = NewRequest(t, "GET", planningv1.BasePath+"/issue-type-assignments?repo_id=1&issue_ids=4").AddTokenAuth(readerToken)
	var rows []struct {
		IssueID int64 `json:"issue_id"`
	}
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &rows)
	assert.Empty(t, rows, "issue 4 belongs to repo2, not the requested repo1")

	// repo2 is private and user4 cannot read it, so naming it directly is 404.
	req = NewRequest(t, "GET", planningv1.BasePath+"/issue-type-assignments?repo_id=2&issue_ids=4").AddTokenAuth(readerToken)
	DecodeJSON(t, MakeRequest(t, req, http.StatusNotFound), &refusal)
	assert.Equal(t, "repo_not_found", refusal.Code)
}

// TestPlanningIssueTypeSetRefusesATypeFromAnotherRepo: type_not_visible, and nothing written.
func TestPlanningIssueTypeSetRefusesATypeFromAnotherRepo(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	foreign := issueType(t, 2, "bug", "#d1242f", "octicon-bug", 3) // scoped to repo2, not repo1

	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/1/type",
		map[string]any{"repo": "user2/repo1", "type_id": foreign.ID}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "type_not_visible", refusal.Code)
	unittest.AssertNotExistsBean(t, &planning_model.IssueTypeAssignment{IssueID: 1, TypeID: foreign.ID})
}

// TestPlanningIssueTypeCreateOrgScopedRequiresOwner: a plain org member is refused, an owner
// succeeds. user4 belongs to org3 through team1 but does not own it; user2 does.
func TestPlanningIssueTypeCreateOrgScopedRequiresOwner(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	memberToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issue-types",
		map[string]any{"org_id": 3, "name": "spike", "color": "#bf8700", "icon": "octicon-beaker", "rank": 3}).AddTokenAuth(memberToken)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusForbidden), &refusal)
	assert.Equal(t, "forbidden", refusal.Code)
	unittest.AssertNotExistsBean(t, &planning_model.IssueType{OrgID: 3, Name: "spike"})

	ownerToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	req = NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issue-types",
		map[string]any{"org_id": 3, "name": "spike", "color": "#bf8700", "icon": "octicon-beaker", "rank": 3}).AddTokenAuth(ownerToken)
	var created issueTypeRowPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &created)
	assert.Equal(t, "org", created.Scope)
	assert.EqualValues(t, 3, created.ScopeID)
	unittest.AssertExistsAndLoadBean(t, &planning_model.IssueType{ID: created.ID, OrgID: 3, Name: "spike"})
}
