// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	auth_model "gitea.dev/models/auth"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"
	"gitea.dev/models/unittest"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planningPagePost posts one of the planning page's own writes as a signed-in browser would:
// form-encoded, with the Sec-Fetch-Site value a real browser sends, so Gitea's cross-origin
// protection sees the request the way it sees any other same-origin fetch.
func planningPagePost(t *testing.T, session *TestSession, path string, values map[string]string, secFetchSite string, expectedStatus int) *httptest.ResponseRecorder {
	t.Helper()
	req := NewRequestWithValues(t, "POST", "/planning/"+path, values).SetHeader("Sec-Fetch-Site", secFetchSite)
	return session.MakeRequest(t, req, expectedStatus)
}

// jsonRedirectPayload is what a successful planning page write answers with.
type jsonRedirectPayload struct {
	Redirect string `json:"redirect"`
}

// jsonErrorPayload is what ctx.JSONError answers with.
type jsonErrorPayload struct {
	ErrorMessage string `json:"errorMessage"`
}

// TestPlanningIssuePageScheduleSetAndClear covers the schedule write's round trip: a set
// leaves a recorded row, a clear removes it.
func TestPlanningIssuePageScheduleSetAndClear(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")

	resp := planningPagePost(t, session, "issues/1/schedule", map[string]string{"start": "2026-03-01"}, "same-origin", http.StatusOK)
	DecodeJSON(t, resp, &jsonRedirectPayload{})
	unittest.AssertExistsAndLoadBean(t, &planning_model.IssueSchedule{IssueID: 1})

	planningPagePost(t, session, "issues/1/schedule", map[string]string{"start": ""}, "same-origin", http.StatusOK)
	unittest.AssertNotExistsBean(t, &planning_model.IssueSchedule{IssueID: 1})
}

// TestPlanningIssuePageTypeSetAndClear covers the type write's round trip.
func TestPlanningIssuePageTypeSetAndClear(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	ty := issueType(t, 1, "page-type", "#112233", "octicon-issue-opened", 1)

	planningPagePost(t, session, "issues/1/type", map[string]string{"type_id": strconv.FormatInt(ty.ID, 10)}, "same-origin", http.StatusOK)
	assigned := unittest.AssertExistsAndLoadBean(t, &planning_model.IssueTypeAssignment{IssueID: 1})
	assert.Equal(t, ty.ID, assigned.TypeID)

	planningPagePost(t, session, "issues/1/type", map[string]string{"type_id": ""}, "same-origin", http.StatusOK)
	unittest.AssertNotExistsBean(t, &planning_model.IssueTypeAssignment{IssueID: 1})
}

// TestPlanningIssuePageParentSetAndClear covers the parent write's round trip: parent is sent
// as "#N", N being the parent's own number in the shared repository, not its global id.
func TestPlanningIssuePageParentSetAndClear(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	parentType := issueType(t, 1, "page-parent-type", "#112233", "octicon-issue-opened", 1)
	childType := issueType(t, 1, "page-child-type", "#112233", "octicon-issue-opened", 2)
	require.NoError(t, planning_model.UpsertAssignment(t.Context(), 5, parentType.ID))
	require.NoError(t, planning_model.UpsertAssignment(t.Context(), 1, childType.ID))

	// Issue 5 is repo1's issue #4, deliberately not global id 4 (which is repo2's private
	// issue): parent="#4" must resolve by repo1's own numbering, not the global id.
	planningPagePost(t, session, "issues/1/parent", map[string]string{"parent": "#4"}, "same-origin", http.StatusOK)
	linked := unittest.AssertExistsAndLoadBean(t, &planning_model.IssueParent{ChildIssueID: 1})
	assert.Equal(t, int64(5), linked.ParentIssueID)

	planningPagePost(t, session, "issues/1/parent", map[string]string{"parent": ""}, "same-origin", http.StatusOK)
	unittest.AssertNotExistsBean(t, &planning_model.IssueParent{ChildIssueID: 1})
}

// TestPlanningIssuePageFieldsSetsAValueRow applies field_<key> members as one partial update.
func TestPlanningIssuePageFieldsSetsAValueRow(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	session := loginUser(t, "user2")
	field := createField(t, token, map[string]any{"repo_id": 1, "key": "points", "label": "Points", "kind": "int"})

	planningPagePost(t, session, "issues/1/fields", map[string]string{"field_points": "5"}, "same-origin", http.StatusOK)
	value := unittest.AssertExistsAndLoadBean(t, &planning_model.FieldValue{IssueID: 1, FieldID: field.ID})
	assert.EqualValues(t, 5, value.ValueInt)
}

// TestPlanningIssuePageEstimateUpdatesTheIssue covers the estimate write.
func TestPlanningIssuePageEstimateUpdatesTheIssue(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	planningPagePost(t, session, "issues/1/estimate", map[string]string{"time_estimate": "4h30m"}, "same-origin", http.StatusOK)

	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	assert.EqualValues(t, 4*3600+30*60, issue.TimeEstimate)
}

// TestPlanningMilestonePageScheduleSetAndClear covers the milestone schedule write's round trip.
func TestPlanningMilestonePageScheduleSetAndClear(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")

	planningPagePost(t, session, "milestones/1/schedule", map[string]string{"start": "2026-02-01"}, "same-origin", http.StatusOK)
	unittest.AssertExistsAndLoadBean(t, &planning_model.MilestoneSchedule{MilestoneID: 1})

	planningPagePost(t, session, "milestones/1/schedule", map[string]string{"start": ""}, "same-origin", http.StatusOK)
	unittest.AssertNotExistsBean(t, &planning_model.MilestoneSchedule{MilestoneID: 1})
}

// TestPlanningIssuePageReaderRefusedWritesNothing: user4 has read but not write access to
// repo1's Issues unit. Every one of the page's writes refuses with 403 and leaves no row.
func TestPlanningIssuePageReaderRefusedWritesNothing(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user4")

	cases := []struct {
		name   string
		path   string
		values map[string]string
	}{
		{"schedule", "issues/1/schedule", map[string]string{"start": "2026-03-01"}},
		{"type", "issues/1/type", map[string]string{"type_id": "1"}},
		{"parent", "issues/1/parent", map[string]string{"parent": "#4"}},
		{"fields", "issues/1/fields", map[string]string{"field_points": "5"}},
		{"estimate", "issues/1/estimate", map[string]string{"time_estimate": "1h"}},
		{"milestone-schedule", "milestones/1/schedule", map[string]string{"start": "2026-02-01"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			planningPagePost(t, session, c.path, c.values, "same-origin", http.StatusForbidden)
		})
	}
	unittest.AssertNotExistsBean(t, &planning_model.IssueSchedule{IssueID: 1})
	unittest.AssertNotExistsBean(t, &planning_model.IssueTypeAssignment{IssueID: 1})
	unittest.AssertNotExistsBean(t, &planning_model.IssueParent{ChildIssueID: 1})
	unittest.AssertNotExistsBean(t, &planning_model.MilestoneSchedule{MilestoneID: 1})
}

// TestPlanningIssuePageCrossSiteRequestRefused: Gitea's own cross-origin protection, not a
// token, guards these routes. A cross-site fetch is refused and writes nothing.
func TestPlanningIssuePageCrossSiteRequestRefused(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	planningPagePost(t, session, "issues/1/schedule", map[string]string{"start": "2026-03-01"}, "cross-site", http.StatusForbidden)
	unittest.AssertNotExistsBean(t, &planning_model.IssueSchedule{IssueID: 1})
}

// TestPlanningIssuePagePrivateRepoOutsiderNotFound: issue 4 belongs to repo2, private to
// user2; user4 cannot see it, so the refusal is 404 and never names the repository.
func TestPlanningIssuePagePrivateRepoOutsiderNotFound(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user4")
	resp := planningPagePost(t, session, "issues/4/schedule", map[string]string{"start": "2026-03-01"}, "same-origin", http.StatusNotFound)
	assert.NotContains(t, resp.Body.String(), "repo2")
}

// TestPlanningIssuePagePullRequestSurfacesTheServiceRefusal: issue 2 is repo1's pull request.
// SetIssueStart's not_an_issue refusal reaches the page as a JSON error, not a redirect.
func TestPlanningIssuePagePullRequestSurfacesTheServiceRefusal(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	resp := planningPagePost(t, session, "issues/2/schedule", map[string]string{"start": "2026-03-01"}, "same-origin", http.StatusBadRequest)
	payload := DecodeJSON(t, resp, &jsonErrorPayload{})
	assert.Contains(t, payload.ErrorMessage, "pull request")
	unittest.AssertNotExistsBean(t, &planning_model.IssueSchedule{IssueID: 2})
}
