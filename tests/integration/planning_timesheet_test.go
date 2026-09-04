// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"
	"time"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	planningv1 "gitea.dev/routers/api/planning/v1"
	issue_service "gitea.dev/services/issue"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// timesheetIssue creates a bare issue under repo, assigned to assigneeIDs, so a test logs time
// against something of its own rather than a fixture issue shared with every other test.
func timesheetIssue(t *testing.T, repo *repo_model.Repository, poster *user_model.User, assigneeIDs []int64) *issues_model.Issue {
	t.Helper()
	issue := &issues_model.Issue{RepoID: repo.ID, PosterID: poster.ID, Title: "timesheet issue", IsPull: false}
	require.NoError(t, issue_service.NewIssue(t.Context(), repo, issue, nil, nil, assigneeIDs, nil))
	return issue
}

// insertTrackedTime writes a tracked-time row at an exact createdUnix. AddTime's own created
// argument is silently overwritten by xorm's own "created" column magic at insert time, so a
// window test needs NoAutoTime to place a row on a chosen day at all.
func insertTrackedTime(t *testing.T, issueID, userID, seconds, createdUnix int64, deleted bool) *issues_model.TrackedTime {
	t.Helper()
	tt := &issues_model.TrackedTime{IssueID: issueID, UserID: userID, Time: seconds, CreatedUnix: createdUnix, Deleted: deleted}
	_, err := db.GetEngine(t.Context()).NoAutoTime().Insert(tt)
	require.NoError(t, err)
	return tt
}

type timesheetEntryPayload struct {
	ID          int64  `json:"id"`
	IssueID     int64  `json:"issue_id"`
	Number      int64  `json:"number"`
	Title       string `json:"title"`
	Seconds     int64  `json:"seconds"`
	CreatedUnix int64  `json:"created_unix"`
	Editable    bool   `json:"editable"`
}

type timesheetDayPayload struct {
	Unix    int64                   `json:"unix"`
	Seconds int64                   `json:"seconds"`
	Entries []timesheetEntryPayload `json:"entries"`
}

type timesheetLanePayload struct {
	UserID       int64                 `json:"user_id"`
	Login        string                `json:"login"`
	Days         []timesheetDayPayload `json:"days"`
	TotalSeconds int64                 `json:"total_seconds"`
}

type timesheetRunningPayload struct {
	UserID      int64  `json:"user_id"`
	Login       string `json:"login"`
	IssueID     int64  `json:"issue_id"`
	Number      int64  `json:"number"`
	Title       string `json:"title"`
	StartedUnix int64  `json:"started_unix"`
}

type timesheetPayload struct {
	RepoID   int64                     `json:"repo_id"`
	FromUnix int64                     `json:"from_unix"`
	ToUnix   int64                     `json:"to_unix"`
	Lanes    []timesheetLanePayload    `json:"lanes"`
	Running  []timesheetRunningPayload `json:"running"`
	Totals   struct {
		ByIssue []struct {
			IssueID int64 `json:"issue_id"`
			Seconds int64 `json:"seconds"`
		} `json:"by_issue"`
		ByUser []struct {
			UserID  int64 `json:"user_id"`
			Seconds int64 `json:"seconds"`
		} `json:"by_user"`
	} `json:"totals"`
	Truncated bool `json:"truncated"`
}

func getTimesheet(t *testing.T, token, query string) (timesheetPayload, hubRefusal, int) {
	t.Helper()
	req := NewRequest(t, "GET", planningv1.BasePath+"/timesheet?"+query).AddTokenAuth(token)
	resp := MakeRequest(t, req, NoExpectedStatus)
	if resp.Code == http.StatusOK {
		var payload timesheetPayload
		DecodeJSON(t, resp, &payload)
		return payload, hubRefusal{}, resp.Code
	}
	refusal := DecodeJSON(t, resp, &hubRefusal{})
	return timesheetPayload{}, *refusal, resp.Code
}

func timesheetLaneFor(payload timesheetPayload, userID int64) (timesheetLanePayload, bool) {
	for _, l := range payload.Lanes {
		if l.UserID == userID {
			return l, true
		}
	}
	return timesheetLanePayload{}, false
}

func timesheetDayFor(lane timesheetLanePayload, unix int64) (timesheetDayPayload, bool) {
	for _, d := range lane.Days {
		if d.Unix == unix {
			return d, true
		}
	}
	return timesheetDayPayload{}, false
}

// TestPlanningTimesheetEntriesScopedToWindowRepoAndNotDeleted: only the in-window, same-repo,
// non-deleted rows reach the lane, its day, and the totals; an out-of-window row, another
// repo's row, and a deleted row are each excluded rather than inflating a total.
func TestPlanningTimesheetEntriesScopedToWindowRepoAndNotDeleted(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	repo2 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 2})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	from := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC) // Monday
	to := from.AddDate(0, 0, 6)                         // Sunday
	monday := from
	wednesday := from.AddDate(0, 0, 2)

	issueMon := timesheetIssue(t, repo1, user2, []int64{2})
	issueWed := timesheetIssue(t, repo1, user2, []int64{2})
	issueOther := timesheetIssue(t, repo2, user2, []int64{2})

	inWindowMon := insertTrackedTime(t, issueMon.ID, 2, 100, monday.Unix(), false)
	insertTrackedTime(t, issueWed.ID, 2, 50, wednesday.Unix(), false)
	insertTrackedTime(t, issueMon.ID, 2, 999, from.AddDate(0, 0, -10).Unix(), false) // outside the window
	insertTrackedTime(t, issueOther.ID, 2, 777, monday.Unix(), false)                // a different repo
	insertTrackedTime(t, issueMon.ID, 2, 333, monday.Unix(), true)                   // deleted

	payload, refusal, status := getTimesheet(t, token, "repo_id=1&from="+from.Format(time.DateOnly)+"&to="+to.Format(time.DateOnly))
	require.Equal(t, http.StatusOK, status, "%+v", refusal)

	lane, ok := timesheetLaneFor(payload, 2)
	require.True(t, ok)
	assert.Equal(t, int64(150), lane.TotalSeconds, "only the two in-window, same-repo, non-deleted rows count")

	mondayPayload, ok := timesheetDayFor(lane, monday.Unix())
	require.True(t, ok)
	require.Len(t, mondayPayload.Entries, 1, "the out-of-window and deleted rows on the same day are absent")
	assert.Equal(t, inWindowMon.ID, mondayPayload.Entries[0].ID)
	assert.Equal(t, int64(100), mondayPayload.Entries[0].Seconds)

	wedPayload, ok := timesheetDayFor(lane, wednesday.Unix())
	require.True(t, ok)
	require.Len(t, wedPayload.Entries, 1, "the Wednesday entry lands on its own day, not Monday's")
	assert.Equal(t, int64(50), wedPayload.Entries[0].Seconds)

	var issueMonTotal, userTotal int64
	for _, it := range payload.Totals.ByIssue {
		if it.IssueID == issueMon.ID {
			issueMonTotal = it.Seconds
		}
	}
	for _, ut := range payload.Totals.ByUser {
		if ut.UserID == 2 {
			userTotal = ut.Seconds
		}
	}
	assert.Equal(t, int64(100), issueMonTotal, "by_issue excludes the out-of-window and deleted rows on the same issue")
	assert.Equal(t, int64(150), userTotal, "by_user excludes the other repo's row and the deleted row")
}

// TestPlanningTimesheetRunningListsStopwatch: a stopwatch running on the repository's own issue
// appears in running, naming the user and the issue.
func TestPlanningTimesheetRunningListsStopwatch(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	issue := timesheetIssue(t, repo1, user2, []int64{2})
	ok, err := issues_model.CreateIssueStopwatch(t.Context(), user2, issue)
	require.NoError(t, err)
	require.True(t, ok)

	payload, refusal, status := getTimesheet(t, token, "repo_id=1")
	require.Equal(t, http.StatusOK, status, "%+v", refusal)

	found := false
	for _, r := range payload.Running {
		if r.IssueID == issue.ID {
			found = true
			assert.Equal(t, int64(2), r.UserID)
			assert.Equal(t, "user2", r.Login)
		}
	}
	assert.True(t, found, "the running stopwatch is listed")
}

// TestPlanningTimesheetEditableIsOwnerOrIssuesWrite: user40 owns the entry and may always edit
// their own. Hardcoding editable true would leave this the only assertion able to catch it.
func TestPlanningTimesheetEditableIsOwnerOrIssuesWrite(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	issue := timesheetIssue(t, repo1, user2, []int64{40})
	insertTrackedTime(t, issue.ID, 40, 100, time.Now().Unix(), false)

	ownerToken := getTokenForLoggedInUser(t, loginUser(t, "user40"), auth_model.AccessTokenScopeAll)
	payload, refusal, status := getTimesheet(t, ownerToken, "repo_id=1")
	require.Equal(t, http.StatusOK, status, "%+v", refusal)
	lane, ok := timesheetLaneFor(payload, 40)
	require.True(t, ok)
	require.Len(t, lane.Days, 7)
	entry := findTimesheetEntry(lane, issue.ID)
	require.NotNil(t, entry, "the entry is on some day in the default current-week window")
	assert.True(t, entry.Editable, "the entry's own owner may always edit it")
}

// TestPlanningTimesheetReaderSeesOnlyOwnLane: user4, a plain reader with neither ownership nor
// Issues write on repo1, gets no lane at all for another user when user_id is omitted — their
// own user_id is forced in rather than the other lane being rendered empty or with entries
// marked not editable. Naming another user explicitly is refused, not silently narrowed.
func TestPlanningTimesheetReaderSeesOnlyOwnLane(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	issue := timesheetIssue(t, repo1, user2, []int64{40})
	insertTrackedTime(t, issue.ID, 40, 100, time.Now().Unix(), false)

	readerToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	payload, refusal, status := getTimesheet(t, readerToken, "repo_id=1")
	require.Equal(t, http.StatusOK, status, "%+v", refusal)
	_, hasUser40 := timesheetLaneFor(payload, 40)
	assert.False(t, hasUser40, "a plain reader without ownership or Issues write cannot see another user's lane at all")
	lane, ok := timesheetLaneFor(payload, 4)
	require.True(t, ok, "the reader's own lane is still present, forced in place of the omitted user_id")
	assert.Equal(t, int64(0), lane.TotalSeconds, "user4 logged nothing, so their own lane is empty")

	_, refusal, status = getTimesheet(t, readerToken, "repo_id=1&user_id=40")
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "not_allowed_to_query_user", refusal.Code)
}

func findTimesheetEntry(lane timesheetLanePayload, issueID int64) *timesheetEntryPayload {
	for _, d := range lane.Days {
		for _, e := range d.Entries {
			if e.IssueID == issueID {
				e := e
				return &e
			}
		}
	}
	return nil
}

// TestPlanningTimesheetUserIDFiltersLanes: user_id restricts the lane set to that one user,
// dropping repo1's other assignee even though they logged time in the same window.
func TestPlanningTimesheetUserIDFiltersLanes(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	issue2 := timesheetIssue(t, repo1, user2, []int64{2})
	issue40 := timesheetIssue(t, repo1, user2, []int64{40})
	now := time.Now().Unix()
	insertTrackedTime(t, issue2.ID, 2, 60, now, false)
	insertTrackedTime(t, issue40.ID, 40, 60, now, false)

	payload, refusal, status := getTimesheet(t, token, "repo_id=1&user_id=2")
	require.Equal(t, http.StatusOK, status, "%+v", refusal)

	_, hasUser2 := timesheetLaneFor(payload, 2)
	_, hasUser40 := timesheetLaneFor(payload, 40)
	assert.True(t, hasUser2, "the requested user still gets a lane")
	assert.False(t, hasUser40, "user_id drops every other assignee's lane")
}

// TestPlanningTimesheetWindowBoundary: 92 days is accepted, 93 is refused bad_window.
func TestPlanningTimesheetWindowBoundary(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	from := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)

	_, refusal, status := getTimesheet(t, token, "repo_id=1&from="+from.Format(time.DateOnly)+"&to="+from.AddDate(0, 0, 91).Format(time.DateOnly))
	require.Equal(t, http.StatusOK, status, "92 days inclusive is accepted: %+v", refusal)

	_, refusal, status = getTimesheet(t, token, "repo_id=1&from="+from.Format(time.DateOnly)+"&to="+from.AddDate(0, 0, 92).Format(time.DateOnly))
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "bad_window", refusal.Code)
}

// TestPlanningTimesheetPrivateRepoOutsiderNotFound: repo2 is private to user2; user4 cannot see
// it, so the refusal is 404 and never names the repository.
func TestPlanningTimesheetPrivateRepoOutsiderNotFound(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	_, refusal, status := getTimesheet(t, token, "repo_id=2")
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "repo_not_found", refusal.Code)
	assert.NotContains(t, refusal.Message, "repo2")
}

// TestPlanningTimesheetMissingRepoIDRefused: repo_id is required.
func TestPlanningTimesheetMissingRepoIDRefused(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	_, refusal, status := getTimesheet(t, token, "")
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "missing_repo_id", refusal.Code)
}

// TestPlanningTimesheetSixtyEntriesInOneDayAllReturned: every one of 60 same-day rows reaches
// the lane, its day, and the totals, proving the response is not silently capped at Gitea's own
// 50-row API default the way a page-size ceiling meant for other endpoints would.
func TestPlanningTimesheetSixtyEntriesInOneDayAllReturned(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	issue := timesheetIssue(t, repo1, user2, []int64{2})
	day := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	const count = 60
	var wantTotal int64
	for i := range count {
		insertTrackedTime(t, issue.ID, 2, int64(10+i), day.Unix(), false)
		wantTotal += int64(10 + i)
	}

	payload, refusal, status := getTimesheet(t, token, "repo_id=1&from="+day.Format(time.DateOnly)+"&to="+day.Format(time.DateOnly))
	require.Equal(t, http.StatusOK, status, "%+v", refusal)
	assert.False(t, payload.Truncated)

	lane, ok := timesheetLaneFor(payload, 2)
	require.True(t, ok)
	dayPayload, ok := timesheetDayFor(lane, day.Unix())
	require.True(t, ok)
	assert.Len(t, dayPayload.Entries, count, "all 60 same-day rows are returned")
	assert.Equal(t, wantTotal, dayPayload.Seconds)
	assert.Equal(t, wantTotal, lane.TotalSeconds)

	var issueTotal int64
	for _, it := range payload.Totals.ByIssue {
		if it.IssueID == issue.ID {
			issueTotal = it.Seconds
		}
	}
	assert.Equal(t, wantTotal, issueTotal)
}

// TestPlanningTimesheetCapTruncatesPastLoweredLimit lowers MaxTimesheetEntries for its own
// duration so the truncation path is proven without seeding a database with 5000 real rows.
func TestPlanningTimesheetCapTruncatesPastLoweredLimit(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	original := planningv1.MaxTimesheetEntries
	planningv1.MaxTimesheetEntries = 10
	defer func() { planningv1.MaxTimesheetEntries = original }()

	repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	issue := timesheetIssue(t, repo1, user2, []int64{2})
	day := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	for i := range 15 {
		insertTrackedTime(t, issue.ID, 2, int64(1+i), day.Unix(), false)
	}

	payload, refusal, status := getTimesheet(t, token, "repo_id=1&from="+day.Format(time.DateOnly)+"&to="+day.Format(time.DateOnly))
	require.Equal(t, http.StatusOK, status, "%+v", refusal)
	assert.True(t, payload.Truncated, "15 rows is more than the lowered cap of 10")

	lane, ok := timesheetLaneFor(payload, 2)
	require.True(t, ok)
	dayPayload, ok := timesheetDayFor(lane, day.Unix())
	require.True(t, ok)
	assert.Len(t, dayPayload.Entries, 10, "the response holds exactly the lowered cap, not all 15 rows")
}
