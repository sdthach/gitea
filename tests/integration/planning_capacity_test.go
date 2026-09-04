// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	auth_model "gitea.dev/models/auth"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/timeutil"
	planningv1 "gitea.dev/routers/api/planning/v1"
	issue_service "gitea.dev/services/issue"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capacityMonday is a fixed working day (Mon-Fri under the default 62 mask) so every test in
// this file spreads load over a known, single-day window.
var capacityMonday = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)

func dateParam(at time.Time) string { return at.Format(time.DateOnly) }

// findMilestoneID looks up a milestone by title in a roadmap response's milestone list, since
// creating one appends to a repository's whole set rather than replying with just the new row.
func findMilestoneID(t *testing.T, milestones []struct {
	MilestoneID int64  `json:"milestone_id"`
	Title       string `json:"title"`
}, title string,
) int64 {
	t.Helper()
	for _, m := range milestones {
		if m.Title == title {
			return m.MilestoneID
		}
	}
	t.Fatalf("no milestone titled %q in the response", title)
	return 0
}

// capacityIssue creates a fully-formed issue directly through Gitea's own service, assigned to
// assigneeIDs, with a recorded schedule start, a deadline and a time estimate — everything
// ResolveBar and the capacity load math read, set once rather than through four round trips.
func capacityIssue(t *testing.T, repo *repo_model.Repository, poster *user_model.User, assigneeIDs []int64, start, end time.Time, estimateSeconds int64) *issues_model.Issue {
	t.Helper()
	ctx := t.Context()
	issue := &issues_model.Issue{RepoID: repo.ID, PosterID: poster.ID, Title: "capacity issue", IsPull: false}
	require.NoError(t, issue_service.NewIssue(ctx, repo, issue, nil, nil, assigneeIDs, nil))
	require.NoError(t, planning_model.UpsertIssueStart(ctx, issue.ID, start.Unix()))
	require.NoError(t, issues_model.UpdateIssueDeadline(ctx, issue, timeutil.TimeStamp(end.Unix()), poster))
	if estimateSeconds > 0 {
		require.NoError(t, issue_service.ChangeTimeEstimate(ctx, issue, poster, estimateSeconds))
	}
	return issue
}

// roadmapCapacityPayload is GET /roadmap/capacity's shape, reduced to what this file asserts on.
type roadmapCapacityPayload struct {
	Lanes []struct {
		UserID              int64   `json:"user_id"`
		Login               string  `json:"login"`
		HoursPerDay         float64 `json:"hours_per_day"`
		Utilization         float64 `json:"utilization"`
		TotalLoadHours      float64 `json:"total_load_hours"`
		TotalAvailableHours float64 `json:"total_available_hours"`
		Over                bool    `json:"over"`
		Days                []struct {
			Unix           int64   `json:"unix"`
			LoadHours      float64 `json:"load_hours"`
			AvailableHours float64 `json:"available_hours"`
			Over           bool    `json:"over"`
		} `json:"days"`
		Unestimated []struct {
			IssueID int64 `json:"issue_id"`
		} `json:"unestimated"`
	} `json:"lanes"`
	Sprints []struct {
		MilestoneID int64  `json:"milestone_id"`
		Title       string `json:"title"`
		WorkingDays int    `json:"working_days"`
		Lanes       []struct {
			UserID         int64   `json:"user_id"`
			LoadHours      float64 `json:"load_hours"`
			AvailableHours float64 `json:"available_hours"`
			Over           bool    `json:"over"`
			Points         float64 `json:"points"`
		} `json:"lanes"`
	} `json:"sprints"`
	SprintsUnscheduled []struct {
		MilestoneID int64    `json:"milestone_id"`
		Missing     []string `json:"missing"`
	} `json:"sprints_unscheduled"`
	Truncated bool `json:"truncated"`
}

func getRoadmapCapacity(t *testing.T, token string, repoID int64, from, to time.Time) roadmapCapacityPayload {
	t.Helper()
	url := planningv1.BasePath + "/roadmap/capacity?repo_id=" + strconv.FormatInt(repoID, 10) +
		"&from=" + dateParam(from) + "&to=" + dateParam(to)
	req := NewRequest(t, "GET", url).AddTokenAuth(token)
	var payload roadmapCapacityPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &payload)
	return payload
}

func laneFor(payload roadmapCapacityPayload, userID int64) (lane struct {
	UserID              int64   `json:"user_id"`
	Login               string  `json:"login"`
	HoursPerDay         float64 `json:"hours_per_day"`
	Utilization         float64 `json:"utilization"`
	TotalLoadHours      float64 `json:"total_load_hours"`
	TotalAvailableHours float64 `json:"total_available_hours"`
	Over                bool    `json:"over"`
	Days                []struct {
		Unix           int64   `json:"unix"`
		LoadHours      float64 `json:"load_hours"`
		AvailableHours float64 `json:"available_hours"`
		Over           bool    `json:"over"`
	} `json:"days"`
	Unestimated []struct {
		IssueID int64 `json:"issue_id"`
	} `json:"unestimated"`
}, ok bool,
) {
	for _, l := range payload.Lanes {
		if l.UserID == userID {
			return l, true
		}
	}
	return lane, false
}

// capacityRowPayload is PUT/DELETE /capacity/{user_id}'s own reply shape.
type capacityRowPayload struct {
	HoursPerDay float64 `json:"hours_per_day"`
	Utilization float64 `json:"utilization"`
	Workdays    int     `json:"workdays"`
	Source      string  `json:"source"`
}

func setCapacity(t *testing.T, token string, userID int64, body map[string]any) (*capacityRowPayload, hubRefusal, int) {
	t.Helper()
	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/capacity/"+strconv.FormatInt(userID, 10), body).AddTokenAuth(token)
	resp := MakeRequest(t, req, NoExpectedStatus)
	if resp.Code == http.StatusOK {
		row := DecodeJSON(t, resp, &capacityRowPayload{})
		return row, hubRefusal{}, resp.Code
	}
	refusal := DecodeJSON(t, resp, &hubRefusal{})
	return nil, *refusal, resp.Code
}

func clearCapacity(t *testing.T, token string, userID int64, body map[string]any) (hubRefusal, int) {
	t.Helper()
	req := NewRequestWithJSON(t, "DELETE", planningv1.BasePath+"/capacity/"+strconv.FormatInt(userID, 10), body).AddTokenAuth(token)
	resp := MakeRequest(t, req, NoExpectedStatus)
	if resp.Code == http.StatusOK {
		return hubRefusal{}, resp.Code
	}
	refusal := DecodeJSON(t, resp, &hubRefusal{})
	return *refusal, resp.Code
}

// capacityListRow is one row of GET /capacity's array response.
type capacityListRow struct {
	UserID int64  `json:"user_id"`
	Source string `json:"source"`
}

func getCapacityList(t *testing.T, token, query string) ([]capacityListRow, hubRefusal, int) {
	t.Helper()
	req := NewRequest(t, "GET", planningv1.BasePath+"/capacity?"+query).AddTokenAuth(token)
	resp := MakeRequest(t, req, NoExpectedStatus)
	if resp.Code == http.StatusOK {
		var rows []capacityListRow
		DecodeJSON(t, resp, &rows)
		return rows, hubRefusal{}, resp.Code
	}
	refusal := DecodeJSON(t, resp, &hubRefusal{})
	return nil, *refusal, resp.Code
}

// createScheduledMilestone creates a milestone with a due date and, when start is non-zero,
// records its own schedule start too, returning its id.
func createScheduledMilestone(t *testing.T, token, title string, start, due time.Time) int64 {
	t.Helper()
	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/milestones",
		map[string]any{"repo": "user2/repo1", "title": title, "end": dateParam(due)}).AddTokenAuth(token)
	var created struct {
		Milestones []struct {
			MilestoneID int64  `json:"milestone_id"`
			Title       string `json:"title"`
		} `json:"milestones"`
	}
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &created)
	id := findMilestoneID(t, created.Milestones, title)

	if !start.IsZero() {
		req = NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/milestones/"+strconv.FormatInt(id, 10)+"/schedule",
			map[string]any{"repo": "user2/repo1", "start": dateParam(start)}).AddTokenAuth(token)
		MakeRequest(t, req, http.StatusOK)
	}
	return id
}

// assignIssueMilestone assigns issueID to milestoneID through the roadmap's own endpoint.
func assignIssueMilestone(t *testing.T, token string, issueID, milestoneID int64) {
	t.Helper()
	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issues/"+strconv.FormatInt(issueID, 10)+"/milestone",
		map[string]any{"repo": "user2/repo1", "milestone_id": milestoneID}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
}

// TestPlanningCapacityRoadmapListsLanesForEveryAssignee: repo1's assignees are its owner
// (user2) and user40, a write collaborator. Only user2 carries load; user40 still gets a lane,
// with zero load, which is what "for every GetRepoAssignees user" means at the wire.
func TestPlanningCapacityRoadmapListsLanesForEveryAssignee(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	capacityIssue(t, repo, user2, []int64{2}, capacityMonday, capacityMonday, 8*3600)

	payload := getRoadmapCapacity(t, token, 1, capacityMonday, capacityMonday)
	lane2, ok := laneFor(payload, 2)
	require.True(t, ok, "user2 has a lane")
	assert.Greater(t, lane2.TotalLoadHours, 0.0)

	lane40, ok := laneFor(payload, 40)
	require.True(t, ok, "user40 has a lane even though no issue is assigned to them")
	assert.Zero(t, lane40.TotalLoadHours)
	assert.False(t, lane40.Over)
}

// TestPlanningCapacityRoadmapFlagsAnOverDay: 6h/day at 50% utilization is 3h available; a
// single issue estimated 16h ("two work days" at the usual 8h/day) assigned to user2 over one
// working day loads that whole day, well past what is available.
func TestPlanningCapacityRoadmapFlagsAnOverDay(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	row, refusal, status := setCapacity(t, token, 2, map[string]any{"repo_id": 1, "hours_per_day": 6, "utilization": 0.5, "workdays": 62})
	require.Equal(t, http.StatusOK, status, "%+v", refusal)
	assert.InEpsilon(t, 6, row.HoursPerDay, 0.001)
	assert.Equal(t, "repo", row.Source)

	capacityIssue(t, repo, user2, []int64{2}, capacityMonday, capacityMonday, 16*3600)

	payload := getRoadmapCapacity(t, token, 1, capacityMonday, capacityMonday)
	lane, ok := laneFor(payload, 2)
	require.True(t, ok)
	require.Len(t, lane.Days, 1)
	assert.InEpsilon(t, 16.0, lane.Days[0].LoadHours, 0.0001)
	assert.InEpsilon(t, 3.0, lane.Days[0].AvailableHours, 0.0001)
	assert.True(t, lane.Days[0].Over)
	assert.True(t, lane.Over)
}

// TestPlanningCapacityRoadmapDividesRemainingAcrossAssignees is mutation proof (b): a 10-hour
// remaining estimate on an issue assigned to both of repo1's assignees splits into 5 hours
// each. Removing the division in capacityLoadItems would double both lanes' load to 10h.
//
// It then logs 4 hours of tracked time against user2 alone: tracked time is subtracted per
// assignee before the split, so only user2's own share shrinks (10h - 4h tracked = 6h, split
// two ways is 3h), while user40's, who tracked nothing, stays at 5h. Subtracting no one's
// tracked time would leave both lanes at 5h; subtracting the sum of every assignee's tracked
// time from the shared pool before splitting would put both lanes at 3h.
func TestPlanningCapacityRoadmapDividesRemainingAcrossAssignees(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	issue := capacityIssue(t, repo, user2, []int64{2, 40}, capacityMonday, capacityMonday, 10*3600)

	payload := getRoadmapCapacity(t, token, 1, capacityMonday, capacityMonday)
	lane2, ok := laneFor(payload, 2)
	require.True(t, ok)
	lane40, ok := laneFor(payload, 40)
	require.True(t, ok)

	require.Len(t, lane2.Days, 1)
	require.Len(t, lane40.Days, 1)
	assert.InEpsilon(t, 5.0, lane2.Days[0].LoadHours, 0.0001, "10h split across 2 assignees is 5h each")
	assert.InEpsilon(t, 5.0, lane40.Days[0].LoadHours, 0.0001)

	_, err := issues_model.AddTime(t.Context(), user2, issue, 4*3600, time.Now())
	require.NoError(t, err)

	payload = getRoadmapCapacity(t, token, 1, capacityMonday, capacityMonday)
	lane2, ok = laneFor(payload, 2)
	require.True(t, ok)
	lane40, ok = laneFor(payload, 40)
	require.True(t, ok)

	require.Len(t, lane2.Days, 1)
	require.Len(t, lane40.Days, 1)
	assert.InEpsilon(t, 3.0, lane2.Days[0].LoadHours, 0.0001, "user2's own 4h tracked reduces only their own share: (10h-4h)/2 = 3h")
	assert.InEpsilon(t, 5.0, lane40.Days[0].LoadHours, 0.0001, "user40 tracked nothing, so their share is untouched")
}

// TestPlanningCapacityRoadmapSprintLaneAndUnscheduled covers a milestone carrying both a
// recorded start and a due date folding into one sprint row, and a milestone missing its start
// listed in sprints_unscheduled instead.
func TestPlanningCapacityRoadmapSprintLaneAndUnscheduled(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	sprintStart := capacityMonday
	sprintEnd := capacityMonday.AddDate(0, 0, 4) // Mon-Fri

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/milestones",
		map[string]any{"repo": "user2/repo1", "title": "Capacity Sprint 1", "end": dateParam(sprintEnd)}).AddTokenAuth(token)
	var created struct {
		Milestones []struct {
			MilestoneID int64  `json:"milestone_id"`
			Title       string `json:"title"`
		} `json:"milestones"`
	}
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &created)
	sprintID := findMilestoneID(t, created.Milestones, "Capacity Sprint 1")

	req = NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/milestones/"+strconv.FormatInt(sprintID, 10)+"/schedule",
		map[string]any{"repo": "user2/repo1", "start": dateParam(sprintStart)}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)

	// An unscheduled milestone: a due date, but no recorded start.
	req = NewRequestWithJSON(t, "POST", planningv1.BasePath+"/milestones",
		map[string]any{"repo": "user2/repo1", "title": "Capacity Sprint 2", "end": dateParam(sprintEnd.AddDate(0, 0, 14))}).AddTokenAuth(token)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &created)
	unscheduledID := findMilestoneID(t, created.Milestones, "Capacity Sprint 2")

	issue := capacityIssue(t, repo, user2, []int64{2}, sprintStart.AddDate(0, 0, 1), sprintEnd, 10*3600)
	req = NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issues/"+strconv.FormatInt(issue.ID, 10)+"/milestone",
		map[string]any{"repo": "user2/repo1", "milestone_id": sprintID}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)

	payload := getRoadmapCapacity(t, token, 1, sprintStart, sprintEnd)

	sprintFound := false
	for _, sprint := range payload.Sprints {
		if sprint.MilestoneID == sprintID {
			sprintFound = true
			assert.Equal(t, 5, sprint.WorkingDays)
			require.Len(t, sprint.Lanes, 1)
			assert.Equal(t, int64(2), sprint.Lanes[0].UserID)
			assert.InEpsilon(t, 10.0, sprint.Lanes[0].LoadHours, 0.0001)
			assert.InEpsilon(t, 5*8*0.8, sprint.Lanes[0].AvailableHours, 0.0001)
		}
	}
	assert.True(t, sprintFound, "the fully-scheduled milestone seeds a sprint row")

	unscheduledFound := false
	for _, u := range payload.SprintsUnscheduled {
		if u.MilestoneID == unscheduledID {
			unscheduledFound = true
			assert.Equal(t, []string{"start"}, u.Missing)
		}
	}
	assert.True(t, unscheduledFound, "the milestone with no recorded start is listed as unscheduled")
}

// TestPlanningCapacitySetSelfOK: a non-admin write collaborator may always set their own row.
func TestPlanningCapacitySetSelfOK(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user40"), auth_model.AccessTokenScopeAll)
	row, refusal, status := setCapacity(t, token, 40, map[string]any{"repo_id": 1, "hours_per_day": 7, "utilization": 0.9, "workdays": 31})
	require.Equal(t, http.StatusOK, status, "%+v", refusal)
	assert.InEpsilon(t, 7, row.HoursPerDay, 0.001)
	assert.Equal(t, 31, row.Workdays)
	assert.Equal(t, "repo", row.Source)
	unittest.AssertExistsAndLoadBean(t, &planning_model.UserCapacity{UserID: 40, RepoID: 1})
}

// TestPlanningCapacitySetAnotherUsersRowRefusedForNonAdmin is mutation proof (c): user40 has
// write access to repo1 but does not administer it, so setting user2's row is refused and
// nothing is written. Dropping the self-or-admin check in capacityAllowed turns this red.
func TestPlanningCapacitySetAnotherUsersRowRefusedForNonAdmin(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user40"), auth_model.AccessTokenScopeAll)
	_, refusal, status := setCapacity(t, token, 2, map[string]any{"repo_id": 1, "hours_per_day": 6, "utilization": 0.5, "workdays": 62})
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "forbidden", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)

	unittest.AssertNotExistsBean(t, &planning_model.UserCapacity{UserID: 2, RepoID: 1})
}

// TestPlanningCapacitySetValidationCodesWriteNothing covers each numeric refusal, none of
// which leaves a row behind.
func TestPlanningCapacitySetValidationCodesWriteNothing(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	for _, tc := range []struct {
		name string
		body map[string]any
		code string
	}{
		{"zero hours", map[string]any{"repo_id": 1, "hours_per_day": 0, "utilization": 0.8, "workdays": 62}, "bad_hours"},
		{"25 hours", map[string]any{"repo_id": 1, "hours_per_day": 25, "utilization": 0.8, "workdays": 62}, "bad_hours"},
		{"zero utilization", map[string]any{"repo_id": 1, "hours_per_day": 8, "utilization": 0, "workdays": 62}, "bad_utilization"},
		{"over 1.0 utilization", map[string]any{"repo_id": 1, "hours_per_day": 8, "utilization": 1.5, "workdays": 62}, "bad_utilization"},
		{"workdays 0", map[string]any{"repo_id": 1, "hours_per_day": 8, "utilization": 0.8, "workdays": 0}, "bad_workdays"},
		{"workdays 128", map[string]any{"repo_id": 1, "hours_per_day": 8, "utilization": 0.8, "workdays": 128}, "bad_workdays"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, refusal, status := setCapacity(t, token, 2, tc.body)
			assert.Equal(t, http.StatusUnprocessableEntity, status)
			assert.Equal(t, tc.code, refusal.Code)
			unittest.AssertNotExistsBean(t, &planning_model.UserCapacity{UserID: 2, RepoID: 1})
		})
	}
}

// TestPlanningCapacityRoadmapWindowBoundary is mutation proof (e): 366 days is accepted, 367
// is refused bad_window. Widening maxCapacityWindowDays turns the second case red.
func TestPlanningCapacityRoadmapWindowBoundary(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	from := capacityMonday

	getRoadmapCapacity(t, token, 1, from, from.AddDate(0, 0, 365)) // 366 days inclusive, accepted

	req := NewRequest(t, "GET", planningv1.BasePath+"/roadmap/capacity?repo_id=1&from="+dateParam(from)+"&to="+dateParam(from.AddDate(0, 0, 366))).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "bad_window", refusal.Code)
}

// TestPlanningCapacityRoadmapPrivateRepoOutsiderNotFound: repo2 is private to user2; user4
// cannot see it, so the refusal is 404 rather than 403 and never names the repository.
func TestPlanningCapacityRoadmapPrivateRepoOutsiderNotFound(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	req := NewRequest(t, "GET", planningv1.BasePath+"/roadmap/capacity?repo_id=2").AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusNotFound), &refusal)
	assert.Equal(t, "repo_not_found", refusal.Code)
	assert.NotContains(t, refusal.Message, "repo2")
}

// TestPlanningCapacityRoadmapSprintMatchesByMilestoneNotDate is mutation proof for SprintLoad's
// milestone matching: an issue in milestone A but scheduled during milestone B's window still
// loads A, and an issue with no milestone at all contributes to neither sprint even though its
// own bar sits squarely inside A's window. Matching by date again would swap the two: the
// no-milestone issue would land in A and the milestone-A issue would land in B instead.
func TestPlanningCapacityRoadmapSprintMatchesByMilestoneNotDate(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	aStart, aEnd := capacityMonday, capacityMonday.AddDate(0, 0, 4)                   // week 1, Mon-Fri
	bStart, bEnd := capacityMonday.AddDate(0, 0, 7), capacityMonday.AddDate(0, 0, 11) // week 2, Mon-Fri

	milestoneA := createScheduledMilestone(t, token, "Sprint Match A", aStart, aEnd)
	milestoneB := createScheduledMilestone(t, token, "Sprint Match B", bStart, bEnd)

	// In milestone A, but its own bar sits entirely in B's window.
	issueInA := capacityIssue(t, repo, user2, []int64{2}, bStart, bEnd, 10*3600)
	assignIssueMilestone(t, token, issueInA.ID, milestoneA)

	// No milestone at all, even though its bar sits squarely inside A's window.
	capacityIssue(t, repo, user2, []int64{2}, aStart, aEnd, 20*3600)

	payload := getRoadmapCapacity(t, token, 1, aStart, bEnd)

	for _, sprint := range payload.Sprints {
		switch sprint.MilestoneID {
		case milestoneA:
			require.Len(t, sprint.Lanes, 1, "milestone A carries only the issue actually assigned to it")
			assert.InEpsilon(t, 10.0, sprint.Lanes[0].LoadHours, 0.0001)
		case milestoneB:
			assert.Empty(t, sprint.Lanes, "no issue names milestone B")
		}
	}
}

// TestPlanningCapacityGetByRepoListsBothAssignees is mutation proof for GET /capacity: repo1's
// two assignees, its owner and its write collaborator, are both listed, each with a source.
func TestPlanningCapacityGetByRepoListsBothAssignees(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	rows, refusal, status := getCapacityList(t, token, "repo_id=1")
	require.Equal(t, http.StatusOK, status, "%+v", refusal)

	byUser := map[int64]string{}
	for _, r := range rows {
		byUser[r.UserID] = r.Source
	}
	require.Contains(t, byUser, int64(2), "repo1's owner is listed")
	require.Contains(t, byUser, int64(40), "repo1's write collaborator is listed")
	assert.NotEmpty(t, byUser[2])
	assert.NotEmpty(t, byUser[40])
}

// TestPlanningCapacityGetByPrivateOrgOutsiderNotFound: privated_org (id 23) is private with
// user5 its only member; user2 is neither, so the refusal is 404 and never names the org.
func TestPlanningCapacityGetByPrivateOrgOutsiderNotFound(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	rows, refusal, status := getCapacityList(t, token, "org_id=23")
	assert.Nil(t, rows)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "org_not_found", refusal.Code)
	assert.NotContains(t, refusal.Message, "privated_org")
}

// TestPlanningCapacityGetMissingScopeRefused: neither repo_id nor org_id is missing_scope.
func TestPlanningCapacityGetMissingScopeRefused(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	rows, refusal, status := getCapacityList(t, token, "")
	assert.Nil(t, rows)
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "missing_scope", refusal.Code)
}

// TestPlanningCapacityDeleteAnotherUsersRowRefusedForNonAdmin is mutation proof for
// DELETE /capacity/{user_id}: user40 has write access to repo1 but does not administer it, so
// clearing user2's row is refused and the row survives.
func TestPlanningCapacityDeleteAnotherUsersRowRefusedForNonAdmin(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token2 := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	_, refusal, status := setCapacity(t, token2, 2, map[string]any{"repo_id": 1, "hours_per_day": 6, "utilization": 0.5, "workdays": 62})
	require.Equal(t, http.StatusOK, status, "%+v", refusal)

	token40 := getTokenForLoggedInUser(t, loginUser(t, "user40"), auth_model.AccessTokenScopeAll)
	deleteRefusal, deleteStatus := clearCapacity(t, token40, 2, map[string]any{"repo_id": 1})
	assert.Equal(t, http.StatusForbidden, deleteStatus)
	assert.Equal(t, "forbidden", deleteRefusal.Code)

	unittest.AssertExistsAndLoadBean(t, &planning_model.UserCapacity{UserID: 2, RepoID: 1})
}

// TestPlanningCapacitySetSelfRepoNotFoundWritesNothing is mutation proof for scope resolution
// running before any write: a nonexistent repo_id refuses 404 not_found even for a self write,
// and leaves no row behind.
func TestPlanningCapacitySetSelfRepoNotFoundWritesNothing(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	_, refusal, status := setCapacity(t, token, 2, map[string]any{"repo_id": 999999, "hours_per_day": 6, "utilization": 0.5, "workdays": 62})
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "not_found", refusal.Code)
	unittest.AssertNotExistsBean(t, &planning_model.UserCapacity{UserID: 2, RepoID: 999999})
}

// TestPlanningCapacitySetSelfPrivateRepoOutsiderNotFoundWritesNothing: repo2 is private to
// user2; user4 cannot see it, so even setting their own row there is refused and writes
// nothing.
func TestPlanningCapacitySetSelfPrivateRepoOutsiderNotFoundWritesNothing(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	_, refusal, status := setCapacity(t, token, 4, map[string]any{"repo_id": 2, "hours_per_day": 6, "utilization": 0.5, "workdays": 62})
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "not_found", refusal.Code)
	unittest.AssertNotExistsBean(t, &planning_model.UserCapacity{UserID: 4, RepoID: 2})
}

// TestPlanningCapacitySetSelfOrgNotMemberWritesNothing: privated_org (id 23) has only user5 as
// a member; user2 is neither a member nor able to see it, so their own row there is refused.
func TestPlanningCapacitySetSelfOrgNotMemberWritesNothing(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	_, refusal, status := setCapacity(t, token, 2, map[string]any{"org_id": 23, "hours_per_day": 6, "utilization": 0.5, "workdays": 62})
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "not_found", refusal.Code)
	unittest.AssertNotExistsBean(t, &planning_model.UserCapacity{UserID: 2, OrgID: 23})
}

// TestPlanningCapacitySetByAdminForAssigneeOK: repo1's administrator (its owner) may set
// capacity for user40, a write-access assignee of the repository.
func TestPlanningCapacitySetByAdminForAssigneeOK(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	row, refusal, status := setCapacity(t, token, 40, map[string]any{"repo_id": 1, "hours_per_day": 7, "utilization": 0.9, "workdays": 62})
	require.Equal(t, http.StatusOK, status, "%+v", refusal)
	assert.InEpsilon(t, 7, row.HoursPerDay, 0.001)
}

// TestPlanningCapacitySetByAdminForNonAssigneeRefused is mutation proof for item 8: repo1's
// administrator may set capacity for the repository's own assignees, but not for a user id
// this repository does not involve at all.
func TestPlanningCapacitySetByAdminForNonAssigneeRefused(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	_, refusal, status := setCapacity(t, token, 4, map[string]any{"repo_id": 1, "hours_per_day": 7, "utilization": 0.9, "workdays": 62})
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "not_an_assignee", refusal.Code)
	unittest.AssertNotExistsBean(t, &planning_model.UserCapacity{UserID: 4, RepoID: 1})
}
