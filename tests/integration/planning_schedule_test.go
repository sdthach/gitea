// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	auth_model "gitea.dev/models/auth"
	planning_model "gitea.dev/models/planning"
	"gitea.dev/models/unittest"
	planningv1 "gitea.dev/routers/api/planning/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
)

// issueFacetsPayload is the shape GET/PUT/DELETE .../schedule and .../estimate answer with.
type issueFacetsPayload struct {
	IssueID  int64 `json:"issue_id"`
	RepoID   int64 `json:"repo_id"`
	CanWrite bool  `json:"can_write"`
	Schedule struct {
		StartUnix   int64  `json:"start_unix"`
		StartSource string `json:"start_source"`
	} `json:"schedule"`
	Milestone *struct {
		ID        int64 `json:"id"`
		StartUnix int64 `json:"start_unix"`
		DueUnix   int64 `json:"due_unix"`
	} `json:"milestone"`
	TimeEstimate   int64 `json:"time_estimate"`
	TrackedSeconds int64 `json:"tracked_seconds"`
}

type milestoneSchedulePayload struct {
	MilestoneID int64 `json:"milestone_id"`
	StartUnix   int64 `json:"start_unix"`
	DueUnix     int64 `json:"due_unix"`
}

// TestPlanningScheduleSetAndClearReflectsOnTheRoadmap covers the round trip: a PUT is read
// back both from the facets it replies with and from the roadmap bar it feeds, and a DELETE
// returns the bar to its fallback source.
func TestPlanningScheduleSetAndClearReflectsOnTheRoadmap(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	issue := manageIssue(t, 1, "checkout")
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/1/schedule",
		map[string]any{"repo": "user2/repo1", "start": "2026-03-01"}).AddTokenAuth(token)
	var facets issueFacetsPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &facets)
	assert.Equal(t, "schedule", facets.Schedule.StartSource)
	assert.EqualValues(t, 1772323200, facets.Schedule.StartUnix, "2026-03-01T00:00:00Z")

	req = NewRequest(t, "GET", planningv1.BasePath+"/roadmap?repo_id=1&limit=200").AddTokenAuth(token)
	var roadmap roadmapPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &roadmap)
	_, startUnix, _, startSource, _ := roadmapBar(t, roadmap, issue.ID)
	assert.Equal(t, "schedule", startSource)
	assert.EqualValues(t, 1772323200, startUnix)

	req = NewRequestWithJSON(t, "DELETE", planningv1.BasePath+"/issues/1/schedule",
		map[string]any{"repo": "user2/repo1"}).AddTokenAuth(token)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &facets)
	assert.Equal(t, "issue_created", facets.Schedule.StartSource, "cleared, the bar falls back to creation")
	unittest.AssertNotExistsBean(t, &planning_model.IssueSchedule{IssueID: issue.ID})
}

// TestPlanningScheduleRefusesAStartAfterTheIssuesDeadline is the write's own conflict check:
// a start past the recorded end is refused, and nothing is written.
func TestPlanningScheduleRefusesAStartAfterTheIssuesDeadline(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	manageIssue(t, 1, "checkout")
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	roadmapWrite(t, token, "/issues/1/dates", map[string]any{"repo": "user2/repo1", "end": "2026-01-01"})

	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/1/schedule",
		map[string]any{"repo": "user2/repo1", "start": "2026-02-01"}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "start_after_end", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)
	unittest.AssertNotExistsBean(t, &planning_model.IssueSchedule{IssueID: 1})
}

// TestPlanningScheduleAcceptsAStartEqualToTheDeadline: the boundary itself is a valid
// schedule, not a start-after-end refusal.
func TestPlanningScheduleAcceptsAStartEqualToTheDeadline(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	manageIssue(t, 1, "checkout")
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	roadmapWrite(t, token, "/issues/1/dates", map[string]any{"repo": "user2/repo1", "end": "2026-02-01"})

	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/1/schedule",
		map[string]any{"repo": "user2/repo1", "start": "2026-02-01"}).AddTokenAuth(token)
	var facets issueFacetsPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &facets)
	assert.EqualValues(t, 1769904000, facets.Schedule.StartUnix, "2026-02-01T00:00:00Z")
}

// TestPlanningScheduleRefusesAWriterWithNoAccess is the write's authorization check: a user
// with no write access to the repository is refused, and nothing is written.
func TestPlanningScheduleRefusesAWriterWithNoAccess(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/1/schedule",
		map[string]any{"repo": "user2/repo1", "start": "2026-03-01"}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusForbidden)
	unittest.AssertNotExistsBean(t, &planning_model.IssueSchedule{IssueID: 1})
}

// TestPlanningMilestoneScheduleSetClearAndForeignRepo covers the milestone half: set, clear,
// and the 422 for a milestone that belongs to a different repository, with nothing written.
func TestPlanningMilestoneScheduleSetClearAndForeignRepo(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/milestones/1/schedule",
		map[string]any{"repo": "user2/repo1", "start": "2026-02-01"}).AddTokenAuth(token)
	var schedule milestoneSchedulePayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &schedule)
	assert.EqualValues(t, 1769904000, schedule.StartUnix, "2026-02-01T00:00:00Z")

	req = NewRequestWithJSON(t, "DELETE", planningv1.BasePath+"/milestones/1/schedule",
		map[string]any{"repo": "user2/repo1"}).AddTokenAuth(token)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &schedule)
	assert.Zero(t, schedule.StartUnix)

	// Milestone 4 belongs to repository 42, not repo1.
	req = NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/milestones/4/schedule",
		map[string]any{"repo": "user2/repo1", "start": "2026-02-01"}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "milestone_not_in_repo", refusal.Code)
	unittest.AssertNotExistsBean(t, &planning_model.MilestoneSchedule{MilestoneID: 4})
}

// TestPlanningIssueEstimateSetAndRefusesABadValue covers the estimate write and its refusal.
func TestPlanningIssueEstimateSetAndRefusesABadValue(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/1/estimate",
		map[string]any{"repo": "user2/repo1", "time_estimate": "3h"}).AddTokenAuth(token)
	var facets issueFacetsPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &facets)
	assert.EqualValues(t, 3*3600, facets.TimeEstimate)

	req = NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/1/estimate",
		map[string]any{"repo": "user2/repo1", "time_estimate": "not a duration"}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "bad_estimate", refusal.Code)
}

// TestPlanningIssueFacetsPermissions covers GET /issues/{id}: can_write follows the caller's
// own write access, and a private repository's issue is 404 to an outsider rather than 403,
// so the refusal never confirms the issue exists.
func TestPlanningIssueFacetsPermissions(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	writerToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	readerToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", planningv1.BasePath+"/issues/1").AddTokenAuth(writerToken)
	var facets issueFacetsPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &facets)
	assert.True(t, facets.CanWrite, "user2 owns the repository")

	req = NewRequest(t, "GET", planningv1.BasePath+"/issues/1").AddTokenAuth(readerToken)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &facets)
	assert.False(t, facets.CanWrite, "user4 has no write access to repo1")

	// Issue 4 belongs to repo2, private to user2; user4 cannot even see it exists.
	req = NewRequest(t, "GET", planningv1.BasePath+"/issues/4").AddTokenAuth(readerToken)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusNotFound), &refusal)
	assert.Equal(t, "issue_not_found", refusal.Code)
}
