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
	api "gitea.dev/modules/structs"
	planningv1 "gitea.dev/routers/api/planning/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enableDependencies turns on the Issues unit's dependency feature: repo1's own fixture config
// carries no enable_dependencies key at all, which unmarshals to off rather than the setting's
// own default.
func enableDependencies(t *testing.T, token, repo string) {
	t.Helper()
	req := NewRequestWithJSON(t, "PATCH", "/api/v1/repos/"+repo, map[string]any{
		"has_issues":       true,
		"internal_tracker": map[string]any{"enable_issue_dependencies": true},
	}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
}

// dependencyDelete removes one dependency edge through the endpoint under test.
func dependencyDelete(t *testing.T, token, repo string, issueID, dependencyID int64) roadmapPayload {
	t.Helper()
	req := NewRequestWithJSON(t, "DELETE",
		planningv1.BasePath+"/issues/"+strconv.FormatInt(issueID, 10)+"/dependencies/"+strconv.FormatInt(dependencyID, 10),
		map[string]any{"repo": repo}).AddTokenAuth(token)
	var payload roadmapPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &payload)
	return payload
}

// unreadableIssue creates a private organization repository user2 has no access to, with one
// issue in it, so a dependency naming it exercises the never-reveal rule against a real,
// existing issue rather than a missing id.
func unreadableIssue(t *testing.T) int64 {
	t.Helper()
	admin := NewAPITestContext(t, "user1", "", auth_model.AccessTokenScopeWriteOrganization, auth_model.AccessTokenScopeWriteRepository)
	const orgName = "planningdependencyunreadable"
	doAPICreateOrganization(admin, &api.CreateOrgOption{UserName: orgName, Visibility: "private"})(t)
	var repo api.Repository
	doAPICreateOrganizationRepository(admin, orgName, &api.CreateRepoOption{Name: "secret", AutoInit: true},
		func(_ *testing.T, created api.Repository) { repo = created })(t)

	issue := &issues_model.Issue{RepoID: repo.ID, Index: 1, PosterID: 1, Title: "secret issue"}
	require.NoError(t, db.Insert(t.Context(), issue))
	return issue.ID
}

// TestPlanningDependencyAddDrawsAnArrowAndRemoveErasesIt covers the pair the roadmap's arrow
// comes from: adding records the edge Gitea's own dependency panel would, and removing takes it
// back off, both replying with the chart the write produced.
func TestPlanningDependencyAddDrawsAnArrowAndRemoveErasesIt(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	enableDependencies(t, token, "user2/repo1")
	manageIssue(t, 1)
	manageIssue(t, 5)

	after := roadmapWrite(t, token, "/issues/1/dependencies",
		map[string]any{"repo": "user2/repo1", "depends_on_issue_id": 5})
	require.Len(t, after.Arrows, 1, "the write answers with the chart carrying the new arrow")
	assert.EqualValues(t, 5, after.Arrows[0].FromIssueID, "the blocker comes first on a schedule")
	assert.EqualValues(t, 1, after.Arrows[0].ToIssueID)
	unittest.AssertExistsAndLoadBean(t, &issues_model.IssueDependency{IssueID: 1, DependencyID: 5})

	gone := dependencyDelete(t, token, "user2/repo1", 1, 5)
	assert.Empty(t, gone.Arrows, "the write answers with the chart with the arrow erased")
	unittest.AssertNotExistsBean(t, &issues_model.IssueDependency{IssueID: 1, DependencyID: 5})
}

// TestPlanningDependencyRefusedWhenDisabled asserts the refusal AND that nothing was written.
// repo1's fixtures leave dependencies off, so no setup is needed to reach the refusal.
func TestPlanningDependencyRefusedWhenDisabled(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	manageIssue(t, 1)
	manageIssue(t, 5)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issues/1/dependencies",
		map[string]any{"repo": "user2/repo1", "depends_on_issue_id": 5}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "dependencies_disabled", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")

	unittest.AssertNotExistsBean(t, &issues_model.IssueDependency{IssueID: 1, DependencyID: 5})
}

// TestPlanningDependencyRefusesWithoutIssuesWrite asserts the refusal AND that nothing was
// written: user4 can read user2/repo1, which is public, and cannot write its Issues unit.
func TestPlanningDependencyRefusesWithoutIssuesWrite(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	ownerToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	enableDependencies(t, ownerToken, "user2/repo1")
	manageIssue(t, 1)
	manageIssue(t, 5)
	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issues/1/dependencies",
		map[string]any{"repo": "user2/repo1", "depends_on_issue_id": 5}).AddTokenAuth(outsiderToken)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusForbidden), &refusal)
	assert.Equal(t, "forbidden", refusal.Code)

	unittest.AssertNotExistsBean(t, &issues_model.IssueDependency{IssueID: 1, DependencyID: 5})
}

// TestPlanningDependencyRefusesBadRequests covers every 422 the add answers other than the
// disabled unit, which needs no dependency of its own to reach. issue1 already depends on
// issue5 throughout, so exists and circular are checked against a real edge rather than one
// this test would otherwise have to create per case.
func TestPlanningDependencyRefusesBadRequests(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	enableDependencies(t, token, "user2/repo1")
	manageIssue(t, 1)
	manageIssue(t, 5)
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	require.NoError(t, db.Insert(t.Context(), &issues_model.IssueDependency{UserID: doer.ID, IssueID: 1, DependencyID: 5}))
	unreadable := unreadableIssue(t)

	for _, tc := range []struct {
		name    string
		issueID int64
		depID   int64
		code    string
	}{
		{"exists", 1, 5, "dependency_exists"},
		{"circular", 5, 1, "circular_dependency"},
		{"self", 1, 1, "same_issue"},
		{"cross_repo", 1, 4, "cross_repo"}, // issue4 belongs to repo2, which user2 can read
		{"unreadable", 1, unreadable, "dependency_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issues/"+strconv.FormatInt(tc.issueID, 10)+"/dependencies",
				map[string]any{"repo": "user2/repo1", "depends_on_issue_id": tc.depID}).AddTokenAuth(token)
			var refusal hubRefusal
			DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
			assert.Equal(t, tc.code, refusal.Code)
			assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")
		})
	}

	unittest.AssertExistsAndLoadBean(t, &issues_model.IssueDependency{IssueID: 1, DependencyID: 5})
	count, err := unittest.GetXORMEngine().Count(new(issues_model.IssueDependency))
	require.NoError(t, err)
	assert.EqualValues(t, 1, count, "no case above recorded a second dependency")
}

// TestPlanningDependencyRefusesForbiddenBeforeDisabled pins the refusal order: a reader with no
// Issues write is answered forbidden, never dependencies_disabled, so a caller with no write
// access is never told how the unit is configured. repo1's fixtures leave dependencies off, so
// this also proves the write check runs first.
func TestPlanningDependencyRefusesForbiddenBeforeDisabled(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	manageIssue(t, 1)
	manageIssue(t, 5)
	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issues/1/dependencies",
		map[string]any{"repo": "user2/repo1", "depends_on_issue_id": 5}).AddTokenAuth(outsiderToken)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusForbidden), &refusal)
	assert.Equal(t, "forbidden", refusal.Code)

	unittest.AssertNotExistsBean(t, &issues_model.IssueDependency{IssueID: 1, DependencyID: 5})
}

// TestPlanningDependencyRemoveRefusesCrossRepoUnreadable covers the remove path's own
// never-reveal rule: a dependency planted directly (by the admin, as CreateIssueDependency
// itself does not check readability) into a repository user2 cannot read is answered
// dependency_not_found rather than confirming the row exists, and nothing is written or
// commented as a result of the refused request.
func TestPlanningDependencyRemoveRefusesCrossRepoUnreadable(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	enableDependencies(t, token, "user2/repo1")

	unreadable := unreadableIssue(t)
	issue1 := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	dep := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: unreadable})
	admin := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	require.NoError(t, issues_model.CreateIssueDependency(t.Context(), admin, issue1, dep))

	req := NewRequestWithJSON(t, "DELETE",
		planningv1.BasePath+"/issues/1/dependencies/"+strconv.FormatInt(unreadable, 10),
		map[string]any{"repo": "user2/repo1"}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusNotFound), &refusal)
	assert.Equal(t, "dependency_not_found", refusal.Code)

	unittest.AssertExistsAndLoadBean(t, &issues_model.IssueDependency{IssueID: 1, DependencyID: unreadable})
	unittest.AssertCount(t, &issues_model.Comment{IssueID: unreadable, Type: issues_model.CommentTypeRemoveDependency}, 0)
}

// TestPlanningDependencyRemoveMissingAnswers404: issue1 and issue5 carry no dependency between
// them, so removing one answers not-found rather than silently doing nothing.
func TestPlanningDependencyRemoveMissingAnswers404(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	enableDependencies(t, token, "user2/repo1")
	manageIssue(t, 1)
	manageIssue(t, 5)

	req := NewRequestWithJSON(t, "DELETE", planningv1.BasePath+"/issues/1/dependencies/5",
		map[string]any{"repo": "user2/repo1"}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusNotFound), &refusal)
	assert.Equal(t, "dependency_not_found", refusal.Code)
}
