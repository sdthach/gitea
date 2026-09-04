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
	"gitea.dev/modules/timeutil"
	planningv1 "gitea.dev/routers/api/planning/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hierarchyFacetsPayload is the shape GET/PUT/DELETE .../parent answer with, reduced to the
// hierarchy fields.
type hierarchyFacetsPayload struct {
	IssueID  int64 `json:"issue_id"`
	Schedule struct {
		StartUnix int64 `json:"start_unix"`
	} `json:"schedule"`
	Parent *struct {
		IssueID int64  `json:"issue_id"`
		Number  int64  `json:"number"`
		Title   string `json:"title"`
	} `json:"parent"`
	Children []struct {
		IssueID  int64 `json:"issue_id"`
		Number   int64 `json:"number"`
		IsClosed bool  `json:"is_closed"`
	} `json:"children"`
	Progress struct {
		Total  int `json:"total"`
		Closed int `json:"closed"`
	} `json:"progress"`
}

func getIssueFacets(t *testing.T, token string, issueID int64) hierarchyFacetsPayload {
	t.Helper()
	req := NewRequest(t, "GET", planningv1.BasePath+"/issues/"+strconv.FormatInt(issueID, 10)).AddTokenAuth(token)
	var facets hierarchyFacetsPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &facets)
	return facets
}

// TestPlanningHierarchySetShowsFacetsAndRoadmapThenRemove: setting a parent shows on both the
// issue's own facets and the roadmap's rollup and tree; removing it clears all three.
func TestPlanningHierarchySetShowsFacetsAndRoadmapThenRemove(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	epic := issueType(t, 1, "epic", "#8250df", "octicon-rocket", 1)
	story := issueType(t, 1, "story", "#2da44e", "octicon-tasklist", 3)
	setIssueType(t, token, "user2/repo1", 1, epic.ID)
	setIssueType(t, token, "user2/repo1", 5, story.ID)

	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/5/parent",
		map[string]any{"repo": "user2/repo1", "parent_issue_id": 1}).AddTokenAuth(token)
	var facets hierarchyFacetsPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &facets)
	require.NotNil(t, facets.Parent)
	assert.EqualValues(t, 1, facets.Parent.IssueID)
	unittest.AssertExistsAndLoadBean(t, &planning_model.IssueParent{ChildIssueID: 5, ParentIssueID: 1})

	parentFacets := getIssueFacets(t, token, 1)
	require.Len(t, parentFacets.Children, 1)
	assert.EqualValues(t, 5, parentFacets.Children[0].IssueID)
	assert.Equal(t, 1, parentFacets.Progress.Total)
	assert.Equal(t, 1, parentFacets.Progress.Closed, "issue 5 is already closed in the fixtures")

	roadmap := getRoadmap(t, token, "repo_id=1&limit=200")
	row := roadmap.Rollups[rollupOf(t, roadmap, "parent", "1")]
	assert.Equal(t, 1, row.Children)
	foundEdge := false
	for _, edge := range roadmap.Tree {
		if edge.IssueID == 5 && edge.ParentIssueID == 1 {
			foundEdge = true
		}
	}
	assert.True(t, foundEdge, "the tree publishes the recorded edge")

	req = NewRequestWithJSON(t, "DELETE", planningv1.BasePath+"/issues/5/parent",
		map[string]any{"repo": "user2/repo1"}).AddTokenAuth(token)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &facets)
	assert.Nil(t, facets.Parent)
	unittest.AssertNotExistsBean(t, &planning_model.IssueParent{ChildIssueID: 5})
}

// TestPlanningHierarchyFacetsDropAnEdgeToAnotherRepository: a parent or child row can name an
// issue in a different repository — plan_issue_parent carries no repo check of its own — and
// the facets must not publish it, or reading one repository's issue would leak whether an id
// exists in another, private one.
func TestPlanningHierarchyFacetsDropAnEdgeToAnotherRepository(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// Issue 5 (repo1) is model-inserted as a child of issue 6 (private repo3) — a foreign
	// edge SetIssueParent itself would refuse (cross_repo), but which stored data can still
	// hold.
	require.NoError(t, planning_model.UpsertParent(t.Context(), 5, 6))

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	facets := getIssueFacets(t, token, 5)
	assert.Nil(t, facets.Parent, "an edge to another repository is dropped, not published")
}

// TestPlanningHierarchyBoardAndRoadmapTreeExcludeAForeignEdge: an edge between two issues that
// both belong to a DIFFERENT repository must not appear in this repository's own tree — a
// scoped-by-repo read like ParentMapForRepo could, if it forgot to filter, leak an edge from a
// repository the caller may not even be able to see.
func TestPlanningHierarchyBoardAndRoadmapTreeExcludeAForeignEdge(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// Issues 4 and 7 both belong to repo2 (non-pull, per the fixtures) — neither is repo1's.
	require.NoError(t, planning_model.UpsertParent(t.Context(), 7, 4))

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	board := getBoard(t, token, "repo_id=1&project_id=1&group_by=none")
	for _, edge := range board.Tree {
		assert.False(t, edge.IssueID == 7 && edge.ParentIssueID == 4, "repo1's board tree carries no repo2 edge")
	}

	roadmap := getRoadmap(t, token, "repo_id=1&limit=200")
	for _, edge := range roadmap.Tree {
		assert.False(t, edge.IssueID == 7 && edge.ParentIssueID == 4, "repo1's roadmap tree carries no repo2 edge")
	}
}

// TestPlanningHierarchySetRefusesRankMismatchAndWritesNothing: a story cannot be filed under
// a task — a task outranks nothing, so it cannot be anyone's parent.
func TestPlanningHierarchySetRefusesRankMismatchAndWritesNothing(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	task := issueType(t, 1, "task", "#57606a", "octicon-checklist", 4)
	story := issueType(t, 1, "story", "#2da44e", "octicon-tasklist", 3)
	setIssueType(t, token, "user2/repo1", 1, task.ID)
	setIssueType(t, token, "user2/repo1", 5, story.ID)

	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/5/parent",
		map[string]any{"repo": "user2/repo1", "parent_issue_id": 1}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "rank_mismatch", refusal.Code)
	assert.Contains(t, refusal.Message, "task")
	assert.Contains(t, refusal.Message, "story")
	assert.NotEmpty(t, refusal.SuggestedAction)
	unittest.AssertNotExistsBean(t, &planning_model.IssueParent{ChildIssueID: 5})
}

// TestPlanningHierarchySetRefusesUntypedChildAndWritesNothing: a child with no type at all
// cannot be linked, because hierarchy needs a type on both sides to rank them.
func TestPlanningHierarchySetRefusesUntypedChildAndWritesNothing(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	epic := issueType(t, 1, "epic", "#8250df", "octicon-rocket", 1)
	setIssueType(t, token, "user2/repo1", 1, epic.ID)
	// issue 5 carries no type in the fixtures.

	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/5/parent",
		map[string]any{"repo": "user2/repo1", "parent_issue_id": 1}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "untyped_issue", refusal.Code)
	assert.Contains(t, refusal.Message, "child")
	assert.NotEmpty(t, refusal.SuggestedAction)
	unittest.AssertNotExistsBean(t, &planning_model.IssueParent{ChildIssueID: 5})
}

// TestPlanningHierarchySetIssueTypeRefusesRankMismatchAgainstItsOwnParentEdge is the other half
// of rankAllowedAgainstLinks: changing a CHILD's type must not break RankAllows against a
// parent it already has — the existing coverage only changed the PARENT's own type.
func TestPlanningHierarchySetIssueTypeRefusesRankMismatchAgainstItsOwnParentEdge(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	epic := issueType(t, 1, "epic", "#8250df", "octicon-rocket", 1)
	story := issueType(t, 1, "story", "#2da44e", "octicon-tasklist", 3)
	setIssueType(t, token, "user2/repo1", 1, epic.ID)
	setIssueType(t, token, "user2/repo1", 5, story.ID)
	setIssueParent(t, token, "user2/repo1", 5, 1)

	// Promoting the child to epic (rank 1) too would no longer be outranked by its own parent.
	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/5/type",
		map[string]any{"repo": "user2/repo1", "type_id": epic.ID}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "rank_mismatch", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)
	// The old assignment still stands.
	unittest.AssertExistsAndLoadBean(t, &planning_model.IssueTypeAssignment{IssueID: 5, TypeID: story.ID})
}

// TestPlanningHierarchySetRefusesTooDeepAndWritesNothing builds a chain of 9 typed issues
// through the API — depths 0 through 8, exactly maxHierarchyDepth — then shows a 10th link
// past it is refused for depth rather than rank. Ranks are inserted directly, past what
// CreateType's own 1-9 range would allow, because a chain this long needs 10 distinct
// strictly-increasing ranks to reach the link under test without rank_mismatch firing first.
func TestPlanningHierarchySetRefusesTooDeepAndWritesNothing(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	const firstID = int64(9700)
	const count = 10
	now := timeutil.TimeStampNow()
	rows := make([]*issues_model.Issue, 0, count)
	ids := make([]int64, count)
	for i := range count {
		id := firstID + int64(i)
		ids[i] = id
		rows = append(rows, &issues_model.Issue{
			ID: id, RepoID: 1, Index: id, PosterID: 2, Title: "chain",
			CreatedUnix: now, UpdatedUnix: now,
		})
	}
	require.NoError(t, db.Insert(t.Context(), rows))

	for i, id := range ids {
		ty := issueType(t, 1, "chain"+strconv.FormatInt(id, 10), "#112233", "octicon-issue-opened", i+1)
		setIssueType(t, token, "user2/repo1", id, ty.ID)
	}

	// The first 9 nodes (ids[0..8], depths 0 through 8) are exactly the limit: every link
	// through the API succeeds.
	for i := 1; i < count-1; i++ {
		setIssueParent(t, token, "user2/repo1", ids[i], ids[i-1])
	}

	// The 10th would put a node at depth 9. Its rank (10) still outranks its would-be
	// parent's (9), so RankAllows passes and the refusal is squarely about depth.
	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/"+strconv.FormatInt(ids[count-1], 10)+"/parent",
		map[string]any{"repo": "user2/repo1", "parent_issue_id": ids[count-2]}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "too_deep", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)
	unittest.AssertNotExistsBean(t, &planning_model.IssueParent{ChildIssueID: ids[count-1]})
}

// TestPlanningHierarchySetRefusesCycleAndWritesNothing sets up, directly at the model layer,
// an edge SetIssueParent itself would never have allowed to exist the other way round — issue
// 5 already recorded as a child of issue 1 — and shows the endpoint refuses closing the loop.
func TestPlanningHierarchySetRefusesCycleAndWritesNothing(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	task := issueType(t, 1, "task", "#57606a", "octicon-checklist", 4)
	epic := issueType(t, 1, "epic", "#8250df", "octicon-rocket", 1)
	setIssueType(t, token, "user2/repo1", 1, task.ID)
	setIssueType(t, token, "user2/repo1", 5, epic.ID)
	require.NoError(t, planning_model.UpsertParent(t.Context(), 5, 1)) // 5's parent is 1

	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/1/parent",
		map[string]any{"repo": "user2/repo1", "parent_issue_id": 5}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "cycle", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)
	unittest.AssertNotExistsBean(t, &planning_model.IssueParent{ChildIssueID: 1})
}

// TestPlanningHierarchySetRefusesCrossRepoAndWritesNothing: the parent must belong to the
// same repository as the child.
func TestPlanningHierarchySetRefusesCrossRepoAndWritesNothing(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/1/parent",
		map[string]any{"repo": "user2/repo1", "parent_issue_id": 4}).AddTokenAuth(token) // issue 4 belongs to repo2
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "cross_repo", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)
	unittest.AssertNotExistsBean(t, &planning_model.IssueParent{ChildIssueID: 1})
}

// TestPlanningHierarchySetParentRefusesAnUnreadableParentAsNotFound: a parent that lives in a
// repository the caller cannot read is refused parent_not_found — never cross_repo — so the
// refusal never confirms whether an id exists in a repository hidden from the caller.
//
// repo6 is private, owned by user10, and carries no access grant, collaboration or team
// membership for user2 in the fixtures.
func TestPlanningHierarchySetParentRefusesAnUnreadableParentAsNotFound(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	now := timeutil.TimeStampNow()
	hidden := &issues_model.Issue{
		ID: 9950, RepoID: 6, Index: 1, PosterID: 10, Title: "hidden from user2",
		CreatedUnix: now, UpdatedUnix: now,
	}
	require.NoError(t, db.Insert(t.Context(), hidden))

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/1/parent",
		map[string]any{"repo": "user2/repo1", "parent_issue_id": hidden.ID}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "parent_not_found", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)
	unittest.AssertNotExistsBean(t, &planning_model.IssueParent{ChildIssueID: 1})
}

// TestPlanningHierarchySetRefusedWithoutIssueWrite asserts the refusal AND that nothing was
// written. user4 can read user2/repo1, which is public, and cannot write its Issues unit.
func TestPlanningHierarchySetRefusedWithoutIssueWrite(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/5/parent",
		map[string]any{"repo": "user2/repo1", "parent_issue_id": 1}).AddTokenAuth(outsiderToken)
	MakeRequest(t, req, http.StatusForbidden)
	unittest.AssertNotExistsBean(t, &planning_model.IssueParent{ChildIssueID: 5})
}

// TestPlanningHierarchyBoardGroupsByParentAndRefusesTheRetiredEpicGrouping covers the board's
// own use of the same hierarchy: a child lands under its root's row, and the retired epic
// grouping is refused naming what is accepted now.
func TestPlanningHierarchyBoardGroupsByParentAndRefusesTheRetiredEpicGrouping(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	epic := issueType(t, 1, "epic", "#8250df", "octicon-rocket", 1)
	story := issueType(t, 1, "story", "#2da44e", "octicon-tasklist", 3)
	setIssueType(t, token, "user2/repo1", 1, epic.ID)
	setIssueType(t, token, "user2/repo1", 5, story.ID)
	setIssueParent(t, token, "user2/repo1", 5, 1)

	board := getBoard(t, token, "repo_id=1&project_id=1&group_by=parent")
	assert.Equal(t, "1", groupOf(t, board, 5), "the child lands under its root's own row")

	req := NewRequest(t, "GET", planningv1.BasePath+"/board?repo_id=1&project_id=1&group_by=epic").AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusBadRequest), &refusal)
	assert.Equal(t, "unknown_grouping", refusal.Code)
	assert.Contains(t, refusal.Accepted, "parent")
	assert.NotContains(t, refusal.Accepted, "epic")
}

// TestPlanningHierarchySetIssueTypeRefusesRankMismatchAgainstAnExistingParentEdge is the
// deferred refusal SetIssueType could not enforce before hierarchy existed: changing a
// parent's type must not break RankAllows against a child it already has.
func TestPlanningHierarchySetIssueTypeRefusesRankMismatchAgainstAnExistingParentEdge(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	epic := issueType(t, 1, "epic", "#8250df", "octicon-rocket", 1)
	story := issueType(t, 1, "story", "#2da44e", "octicon-tasklist", 3)
	task := issueType(t, 1, "task", "#57606a", "octicon-checklist", 4)
	setIssueType(t, token, "user2/repo1", 1, epic.ID)
	setIssueType(t, token, "user2/repo1", 5, story.ID)
	setIssueParent(t, token, "user2/repo1", 5, 1)

	// Demoting the parent to task (rank 4) would no longer outrank its story child (rank 3).
	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/1/type",
		map[string]any{"repo": "user2/repo1", "type_id": task.ID}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "rank_mismatch", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)
	// The old assignment still stands.
	unittest.AssertExistsAndLoadBean(t, &planning_model.IssueTypeAssignment{IssueID: 1, TypeID: epic.ID})
}

// TestPlanningHierarchyClearIssueTypeRefusesHasLinks is the other deferred refusal: an issue
// linked as a parent or a child needs a type on both sides to be ranked, so clearing either
// side's type is refused rather than leaving an edge with no rank behind it.
func TestPlanningHierarchyClearIssueTypeRefusesHasLinks(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	epic := issueType(t, 1, "epic", "#8250df", "octicon-rocket", 1)
	story := issueType(t, 1, "story", "#2da44e", "octicon-tasklist", 3)
	setIssueType(t, token, "user2/repo1", 1, epic.ID)
	setIssueType(t, token, "user2/repo1", 5, story.ID)
	setIssueParent(t, token, "user2/repo1", 5, 1)

	for _, tc := range []struct {
		name    string
		issueID int64
		typeID  int64
	}{
		{"parent", 1, epic.ID},
		{"child", 5, story.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := NewRequestWithJSON(t, "DELETE", planningv1.BasePath+"/issues/"+strconv.FormatInt(tc.issueID, 10)+"/type",
				map[string]any{"repo": "user2/repo1"}).AddTokenAuth(token)
			var refusal hubRefusal
			DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
			assert.Equal(t, "has_links", refusal.Code)
			assert.NotEmpty(t, refusal.SuggestedAction)
			// The old assignment still stands.
			unittest.AssertExistsAndLoadBean(t, &planning_model.IssueTypeAssignment{IssueID: tc.issueID, TypeID: tc.typeID})
		})
	}
}
