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
	project_model "gitea.dev/models/project"
	"gitea.dev/models/unittest"
	planningv1 "gitea.dev/routers/api/planning/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// projectsPickerPayload is GET /projects' response, reduced to what these tests assert.
type projectsPickerPayload struct {
	Repos []struct {
		ID              int64  `json:"id"`
		FullName        string `json:"full_name"`
		Private         bool   `json:"private"`
		ProjectsEnabled bool   `json:"projects_enabled"`
	} `json:"repos"`
	Projects []struct {
		ID       int64 `json:"id"`
		RepoID   int64 `json:"repo_id"`
		IsClosed bool  `json:"is_closed"`
	} `json:"projects"`
}

func getProjectsPicker(t *testing.T, token, query string) projectsPickerPayload {
	t.Helper()
	req := NewRequest(t, "GET", planningv1.BasePath+"/projects?"+query).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var page projectsPickerPayload
	DecodeJSON(t, resp, &page)
	return page
}

func hasRepo(page projectsPickerPayload, id int64) bool {
	for _, r := range page.Repos {
		if r.ID == id {
			return true
		}
	}
	return false
}

func hasProject(page projectsPickerPayload, id int64) bool {
	for _, p := range page.Projects {
		if p.ID == id {
			return true
		}
	}
	return false
}

// TestPlanningProjectsPickerListsReadableRepos: without repo_id the picker lists the
// repositories the caller can see and no project — a board is opened only once a
// repository is chosen. repo2 is private to user2 in the fixtures, so user4 must not see it.
func TestPlanningProjectsPickerListsReadableRepos(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	ownerToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	page := getProjectsPicker(t, ownerToken, "limit=50")
	assert.True(t, hasRepo(page, 1), "user2 can read repo1")
	assert.Empty(t, page.Projects, "the picker without repo_id lists no project")

	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	outsiderPage := getProjectsPicker(t, outsiderToken, "limit=50")
	assert.False(t, hasRepo(outsiderPage, 2), "repo2 is private to user2; user4 has no access to it")
}

// TestPlanningProjectsPickerWithRepoIDListsItsProjects covers the second form: one
// repository and the boards filed under it.
func TestPlanningProjectsPickerWithRepoIDListsItsProjects(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	page := getProjectsPicker(t, token, "repo_id=1")
	require.Len(t, page.Repos, 1)
	assert.Equal(t, int64(1), page.Repos[0].ID)

	found := false
	for _, p := range page.Projects {
		if p.ID == 1 {
			found = true
			assert.Equal(t, int64(1), p.RepoID)
			assert.False(t, p.IsClosed)
		}
	}
	assert.True(t, found, "project 1 belongs to repo1 in the fixtures")
}

// TestPlanningProjectsPickerRefusesAPrivateRepoAsAnOutsider: 404 that never names the
// repository, exactly like the board's own guard.
func TestPlanningProjectsPickerRefusesAPrivateRepoAsAnOutsider(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	req := NewRequest(t, "GET", planningv1.BasePath+"/projects?repo_id=2").AddTokenAuth(outsiderToken)
	resp := MakeRequest(t, req, http.StatusNotFound)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "repo_not_found", refusal.Code)
	assert.NotContains(t, refusal.Message, "repo2", "a private repository's existence is hidden")
	assert.NotEmpty(t, refusal.SuggestedAction)
}

// TestPlanningProjectsPickerRepoIDPagesForReal: repo_id's own query used to fetch every
// matching row with db.ListOptionsAll while the response still published total and Link
// headers from the request's own page and limit, so limit=1 answered every project on every
// page. org3's own project is created here so repo_id=3 carries two rows to page over.
func TestPlanningProjectsPickerRepoIDPagesForReal(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	require.NoError(t, project_model.NewProject(t.Context(), &project_model.Project{
		Title: "org3 project", OwnerID: 3, Type: project_model.TypeOrganization, CreatorID: 4,
	}))

	token := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", planningv1.BasePath+"/projects?repo_id=3&limit=1").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	assert.Equal(t, "2", resp.Header().Get("X-Total-Count"), "repo3's own project plus the new org project")
	var page1 projectsPickerPayload
	DecodeJSON(t, resp, &page1)
	require.Len(t, page1.Projects, 1, "limit=1 must not answer with every project")

	req = NewRequest(t, "GET", planningv1.BasePath+"/projects?repo_id=3&limit=1&page=2").AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusOK)
	assert.Equal(t, "2", resp.Header().Get("X-Total-Count"))
	var page2 projectsPickerPayload
	DecodeJSON(t, resp, &page2)
	require.Len(t, page2.Projects, 1)
	assert.NotEqual(t, page1.Projects[0].ID, page2.Projects[0].ID, "page 2 answers a different project than page 1")

	req = NewRequest(t, "GET", planningv1.BasePath+"/projects?repo_id=3&limit=1&page=3").AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusOK)
	var page3 projectsPickerPayload
	DecodeJSON(t, resp, &page3)
	assert.Empty(t, page3.Projects, "a page past the end returns none")
}

// TestPlanningProjectsPickerRepoIDIncludesOrgProjects: repo3 is owned by org3, so its own
// projects appear under repo_id=3 alongside the repository's.
func TestPlanningProjectsPickerRepoIDIncludesOrgProjects(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	require.NoError(t, project_model.NewProject(t.Context(), &project_model.Project{
		Title: "org3 project", OwnerID: 3, Type: project_model.TypeOrganization, CreatorID: 4,
	}))

	token := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	page := getProjectsPicker(t, token, "repo_id=3&limit=50")

	foundOrgProject := false
	for _, p := range page.Projects {
		if p.RepoID == 0 {
			foundOrgProject = true
		}
	}
	assert.True(t, foundOrgProject, "org3's own project appears under repo_id=3")
	assert.True(t, hasProject(page, 2), "repo3's own project 2 still appears")
}

// TestPlanningProjectsPickerFiltersByIsClosed: is_closed=true lists only closed projects,
// not every project of the repository regardless of state.
func TestPlanningProjectsPickerFiltersByIsClosed(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	closed := &project_model.Project{Title: "closed project", RepoID: 1, Type: project_model.TypeRepository, IsClosed: true, CreatorID: 2}
	require.NoError(t, project_model.NewProject(t.Context(), closed))

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	page := getProjectsPicker(t, token, "repo_id=1&is_closed=true&limit=50")

	require.NotEmpty(t, page.Projects)
	for _, p := range page.Projects {
		assert.True(t, p.IsClosed, "is_closed=true must list only closed projects")
	}
	assert.False(t, hasProject(page, 1), "project 1 is open in the fixtures, so it must not appear")
	assert.True(t, hasProject(page, closed.ID))
}

// TestPlanningProjectsPickerFlagsARepoWithProjectsDisabled: repo4 has no Projects unit in
// the fixtures (models/fixtures/repo_unit.yml), so the picker says so rather than defaulting
// projects_enabled to true.
func TestPlanningProjectsPickerFlagsARepoWithProjectsDisabled(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	page := getProjectsPicker(t, token, "limit=50")

	found := false
	for _, r := range page.Repos {
		if r.ID == 4 {
			found = true
			assert.False(t, r.ProjectsEnabled, "repo4 carries no Projects unit in the fixtures")
		}
	}
	assert.True(t, found, "repo4 is public and carries the Issues unit")
}

// projectViewsPayload is every saved-view write's response.
type projectViewsPayload struct {
	Views []struct {
		ID        int64  `json:"id"`
		ProjectID int64  `json:"project_id"`
		Name      string `json:"name"`
		Query     string `json:"query"`
		CreatedBy int64  `json:"created_by"`
	} `json:"views"`
}

func getProjectViews(t *testing.T, token string, projectID int64, repo string) projectViewsPayload {
	t.Helper()
	req := NewRequest(t, "GET", planningv1.BasePath+"/projects/"+strconv.FormatInt(projectID, 10)+"/views?repo="+repo).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var page projectViewsPayload
	DecodeJSON(t, resp, &page)
	return page
}

// TestPlanningProjectViewsCreateThenList exercises the saved-view happy path: a write
// answers with the view list as it now stands, and a subsequent read agrees.
func TestPlanningProjectViewsCreateThenList(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/projects/1/views",
		map[string]any{"repo": "user2/repo1", "name": "open bugs", "query": "state:open"}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var created projectViewsPayload
	DecodeJSON(t, resp, &created)
	require.Len(t, created.Views, 1)
	assert.Equal(t, "open bugs", created.Views[0].Name)
	assert.Equal(t, "state:open", created.Views[0].Query)

	fetched := getProjectViews(t, token, 1, "user2/repo1")
	require.Len(t, fetched.Views, 1)
	assert.Equal(t, created.Views[0].ID, fetched.Views[0].ID)
}

// TestPlanningProjectViewsRefuseAsAnOutsiderOnADifferentRepo: repo2 is private to user2, so
// user4 gets the same hidden-existence 404 listing a project's views naming it that the
// picker gives naming it directly — the ownership check runs before any project lookup.
func TestPlanningProjectViewsRefuseAsAnOutsiderOnADifferentRepo(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	req := NewRequest(t, "GET", planningv1.BasePath+"/projects/1/views?repo=user2/repo2").AddTokenAuth(outsiderToken)
	resp := MakeRequest(t, req, http.StatusNotFound)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "repo_not_found", refusal.Code)
	assert.NotContains(t, refusal.Message, "repo2", "a private repository's existence is hidden")
	assert.NotEmpty(t, refusal.SuggestedAction)
}

// TestPlanningProjectViewsNameLengthBoundary: exactly 100 characters is accepted, 101 is
// refused — the limit is on the name's own length.
func TestPlanningProjectViewsNameLengthBoundary(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/projects/1/views",
		map[string]any{"repo": "user2/repo1", "name": strings.Repeat("a", 100), "query": ""}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)

	req = NewRequestWithJSON(t, "POST", planningv1.BasePath+"/projects/1/views",
		map[string]any{"repo": "user2/repo1", "name": strings.Repeat("b", 101), "query": ""}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusUnprocessableEntity)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "bad_view_name", refusal.Code)

	views, err := planning_model.ListProjectViews(t.Context(), 1)
	require.NoError(t, err)
	assert.Len(t, views, 1, "only the 100-character name was written")
}

// TestPlanningProjectViewsQueryLengthBoundary: exactly 4096 bytes is accepted, 4097 is
// refused.
func TestPlanningProjectViewsQueryLengthBoundary(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/projects/1/views",
		map[string]any{"repo": "user2/repo1", "name": "fits", "query": strings.Repeat("a", 4096)}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)

	req = NewRequestWithJSON(t, "POST", planningv1.BasePath+"/projects/1/views",
		map[string]any{"repo": "user2/repo1", "name": "too long", "query": strings.Repeat("a", 4097)}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusUnprocessableEntity)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "bad_query", refusal.Code)

	views, err := planning_model.ListProjectViews(t.Context(), 1)
	require.NoError(t, err)
	assert.Len(t, views, 1, "only the view with the 4096-byte query was written")
}

// TestPlanningProjectViewsRefuseADuplicateNameCaseInsensitive: "DUP" collides with an
// existing "dup" regardless of the database's own collation.
func TestPlanningProjectViewsRefuseADuplicateNameCaseInsensitive(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/projects/1/views",
		map[string]any{"repo": "user2/repo1", "name": "dup", "query": ""}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)

	req = NewRequestWithJSON(t, "POST", planningv1.BasePath+"/projects/1/views",
		map[string]any{"repo": "user2/repo1", "name": "DUP", "query": ""}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusUnprocessableEntity)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "view_exists", refusal.Code)
}

// TestPlanningProjectViewsRefuseADuplicateName: 422, and the row count is unchanged.
func TestPlanningProjectViewsRefuseADuplicateName(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	body := map[string]any{"repo": "user2/repo1", "name": "open bugs", "query": "state:open"}

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/projects/1/views", body).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)

	req = NewRequestWithJSON(t, "POST", planningv1.BasePath+"/projects/1/views", body).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusUnprocessableEntity)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "view_exists", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)

	views, err := planning_model.ListProjectViews(t.Context(), 1)
	require.NoError(t, err)
	assert.Len(t, views, 1, "the refused duplicate wrote nothing")
}

// TestPlanningProjectViewsRefuseABadName: an empty name is refused before anything is
// written.
func TestPlanningProjectViewsRefuseABadName(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/projects/1/views",
		map[string]any{"repo": "user2/repo1", "name": "  ", "query": ""}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusUnprocessableEntity)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "bad_view_name", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)

	views, err := planning_model.ListProjectViews(t.Context(), 1)
	require.NoError(t, err)
	assert.Empty(t, views, "the refused write wrote nothing")
}

// TestPlanningProjectViewsRefuseAReaderWithoutProjectsWrite: user4 can read the public
// repo1 but writes neither of its units, so the save is refused and nothing is written —
// the same shape as the board's own write guard.
func TestPlanningProjectViewsRefuseAReaderWithoutProjectsWrite(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/projects/1/views",
		map[string]any{"repo": "user2/repo1", "name": "sneaky", "query": ""}).AddTokenAuth(outsiderToken)
	resp := MakeRequest(t, req, http.StatusForbidden)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "forbidden", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)

	unittest.AssertNotExistsBean(t, &planning_model.ProjectView{ProjectID: 1, Name: "sneaky"})
}

// TestPlanningProjectViewsRefuseDeletingAViewFromAnotherProject: view_not_found, and the
// row survives. user2 has write access to both repo1 (owner) and repo3 (collaborator,
// fixtures/collaboration.yml), so the refusal is about project scoping, not permission.
func TestPlanningProjectViewsRefuseDeletingAViewFromAnotherProject(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/projects/1/views",
		map[string]any{"repo": "user2/repo1", "name": "mine", "query": ""}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var created projectViewsPayload
	DecodeJSON(t, resp, &created)
	require.Len(t, created.Views, 1)
	viewID := created.Views[0].ID

	req = NewRequestWithJSON(t, "DELETE", planningv1.BasePath+"/projects/2/views/"+strconv.FormatInt(viewID, 10),
		map[string]any{"repo": "org3/repo3"}).AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusNotFound)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "view_not_found", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)

	unittest.AssertExistsAndLoadBean(t, &planning_model.ProjectView{ID: viewID})
}

// TestPlanningProjectViewsRefuseAProjectNotInTheNamedRepo: project 1 belongs to repo1,
// not repo3 — 422 rather than silently scoping the view to the wrong repository.
func TestPlanningProjectViewsRefuseAProjectNotInTheNamedRepo(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/projects/1/views",
		map[string]any{"repo": "org3/repo3", "name": "wrong repo", "query": ""}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusUnprocessableEntity)
	var refusal hubRefusal
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "project_not_in_repo", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)

	unittest.AssertNotExistsBean(t, &planning_model.ProjectView{ProjectID: 1, Name: "wrong repo"})
}
