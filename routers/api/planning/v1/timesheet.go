// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"time"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	user_model "gitea.dev/models/user"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	"gitea.dev/services/hub/query"
	planning_service "gitea.dev/services/planning"
)

// maxTimesheetWindowDays bounds the Time tab's chart: no client renders one wider than a
// single screen at a time.
const maxTimesheetWindowDays = 92

// MaxTimesheetEntries bounds how many tracked-time rows one response folds in, the same
// truncate-and-flag contract GetRoadmapCapacity's own issue cap carries. A package variable
// rather than a constant so an integration test can lower it and prove the truncation path
// actually triggers without seeding a database past a five-figure row count.
var MaxTimesheetEntries = 5000

// Timesheet is GET /timesheet's response: one lane per user, the timers currently running, and
// totals sliced four ways.
type Timesheet struct {
	RepoID    int64           `json:"repo_id"`
	FromUnix  int64           `json:"from_unix"`
	ToUnix    int64           `json:"to_unix"`
	Lanes     []TimesheetLane `json:"lanes"`
	Running   []TimesheetRow  `json:"running"`
	Totals    TimesheetTotals `json:"totals"`
	Truncated bool            `json:"truncated"`
}

// TimesheetLane is one user's week: their own day-by-day entries and the sum of them.
type TimesheetLane struct {
	UserID       int64          `json:"user_id"`
	Login        string         `json:"login"`
	DisplayName  string         `json:"display_name"`
	AvatarURL    string         `json:"avatar_url"`
	Days         []TimesheetDay `json:"days"`
	TotalSeconds int64          `json:"total_seconds"`
}

// TimesheetDay is one calendar day of one lane.
type TimesheetDay struct {
	Unix    int64            `json:"unix"`
	Seconds int64            `json:"seconds"`
	Entries []TimesheetEntry `json:"entries"`
}

// TimesheetEntry is one tracked-time row. Editable is set when the doer owns the entry or can
// write the Issues unit.
type TimesheetEntry struct {
	ID          int64  `json:"id"`
	IssueID     int64  `json:"issue_id"`
	Number      int64  `json:"number"`
	Title       string `json:"title"`
	Seconds     int64  `json:"seconds"`
	CreatedUnix int64  `json:"created_unix"`
	Editable    bool   `json:"editable"`
}

// TimesheetRow is one running stopwatch.
type TimesheetRow struct {
	UserID      int64  `json:"user_id"`
	Login       string `json:"login"`
	IssueID     int64  `json:"issue_id"`
	Number      int64  `json:"number"`
	Title       string `json:"title"`
	StartedUnix int64  `json:"started_unix"`
}

// TimesheetTotals is the window's totals, each summed over the same entries the lanes publish.
type TimesheetTotals struct {
	ByIssue     []TimesheetIssueTotal     `json:"by_issue"`
	ByUser      []TimesheetUserTotal      `json:"by_user"`
	ByMilestone []TimesheetMilestoneTotal `json:"by_milestone"`
	ByType      []TimesheetTypeTotal      `json:"by_type"`
}

type TimesheetIssueTotal struct {
	IssueID int64  `json:"issue_id"`
	Number  int64  `json:"number"`
	Title   string `json:"title"`
	Seconds int64  `json:"seconds"`
}

type TimesheetUserTotal struct {
	UserID  int64  `json:"user_id"`
	Login   string `json:"login"`
	Seconds int64  `json:"seconds"`
}

type TimesheetMilestoneTotal struct {
	MilestoneID int64  `json:"milestone_id"`
	Title       string `json:"title"`
	Seconds     int64  `json:"seconds"`
}

type TimesheetTypeTotal struct {
	TypeID  int64  `json:"type_id"`
	Name    string `json:"name"`
	Seconds int64  `json:"seconds"`
}

var timesheetSpec = query.Spec{
	Resource: "timesheet",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "from", Column: "from", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
		{Name: "to", Column: "to", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
		{Name: "user_id", Column: "user_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
	},
	PrimaryKey: "repo_id",
}

func getTimesheetEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getTimesheet", Method: http.MethodGet, Path: "/timesheet",
			Summary: "The Time tab's timesheet: tracked time by user and day",
			Description: "repo_id is required and readable the same way GET /issue-types checks it. from and to are " +
				"YYYY-MM-DD UTC dates, inclusive, defaulting to the current ISO week (Monday through Sunday) when " +
				"both are omitted; the window is at most 92 days (bad_window otherwise). user_id restricts the " +
				"response to that one user's own lane and running timer; a doer who is neither a site administrator " +
				"nor an Issues writer on the repository is limited to their own lane regardless of user_id, and " +
				"naming another user answers not_allowed_to_query_user. lanes cover every repository assignee plus " +
				"anyone else who logged time in the window, even one with no entries at all, except a lane the " +
				"caller has no access to query, which is omitted rather than shown empty. entries come from " +
				"Gitea's own time-tracking; a deleted entry never appears. running lists this repository's own " +
				"stopwatches currently ticking. Totals are summed over the same entries the lanes publish. " +
				"truncated marks a window with more than 5000 tracked-time rows, where only a prefix was folded in.",
			Tag: "timesheet", Query: &timesheetSpec, Response: "Timesheet", ResponseIs: "object",
			CLINames: []string{"timesheet"},
		},
		Handler: GetTimesheet,
	}
}

// timesheetWindow resolves and validates the [from, to] window: a caller-supplied date wins,
// the current ISO week fills in whichever side is omitted, and the whole span is checked
// against this endpoint's own cap regardless of where each side came from.
func timesheetWindow(ctx *context.APIContext, fromRaw, toRaw string) (time.Time, time.Time, bool) {
	from, ok := parseCapacityDate(ctx, fromRaw, "from")
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	to, ok := parseCapacityDate(ctx, toRaw, "to")
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	if fromRaw == "" || toRaw == "" {
		weekStart, weekEnd := planning_service.CurrentISOWeek(time.Now())
		if fromRaw == "" {
			from = weekStart
		}
		if toRaw == "" {
			to = weekEnd
		}
	}
	if to.Before(from) {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "bad_window",
			"to is before from", "Swap from and to, or widen the window.")
		return time.Time{}, time.Time{}, false
	}
	if days := windowDays(from, to); days > maxTimesheetWindowDays {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "bad_window",
			fmt.Sprintf("the window is %d days, more than the %d this endpoint reads", days, maxTimesheetWindowDays),
			fmt.Sprintf("Narrow from and to to at most %d days apart.", maxTimesheetWindowDays))
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

// timesheetStopwatch is one running stopwatch on one of the repository's own issues.
type timesheetStopwatch struct {
	UserID      int64
	IssueID     int64
	CreatedUnix int64
}

// timesheetRunningStopwatches reads every stopwatch running on one of repoID's own issues,
// joined through the issue table since Gitea's stopwatch table carries no repo_id of its own.
func timesheetRunningStopwatches(ctx *context.APIContext, repoID int64) ([]timesheetStopwatch, error) {
	sws, err := planning_model.RunningStopwatches(ctx, repoID)
	if err != nil {
		return nil, err
	}
	rows := make([]timesheetStopwatch, 0, len(sws))
	for _, sw := range sws {
		rows = append(rows, timesheetStopwatch{UserID: sw.UserID, IssueID: sw.IssueID, CreatedUnix: int64(sw.CreatedUnix)})
	}
	return rows, nil
}

// GetTimesheet answers GET /timesheet.
func GetTimesheet(ctx *context.APIContext) {
	q, ok := hubapi.ParseQuery(ctx, timesheetSpec)
	if !ok {
		return
	}
	repoID := hubapi.EqualityFilterInt(q, "repo_id")
	if repoID <= 0 {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "missing_repo_id",
			"repo_id is required: the timesheet covers one repository's issues",
			"Pass ?repo_id=<id>, listing "+BasePath+"/repos to find it.")
		return
	}
	repo, perm, ok := issueTypeReadableRepo(ctx, repoID)
	if !ok {
		return
	}
	from, to, ok := timesheetWindow(ctx, hubapi.EqualityFilterString(q, "from"), hubapi.EqualityFilterString(q, "to"))
	if !ok {
		return
	}
	userFilter := hubapi.EqualityFilterInt(q, "user_id")
	canWriteIssues := perm.CanWrite(unit.TypeIssues)

	// A plain reader — neither a site administrator nor an Issues writer — sees only their own
	// lane: user_id is forced to the doer when omitted, and naming anyone else is refused
	// rather than answered with another user's entries.
	cantSetUser := !ctx.Doer.IsAdmin && !canWriteIssues && userFilter != ctx.Doer.ID
	if cantSetUser {
		if userFilter == 0 {
			userFilter = ctx.Doer.ID
		} else {
			hubapi.APIError(ctx, http.StatusForbidden, "not_allowed_to_query_user",
				"query by user not allowed; not enough rights",
				"Omit user_id to read your own lane, or ask a repository Issues writer.")
			return
		}
	}

	opts := &issues_model.FindTrackedTimesOptions{
		ListOptions:       db.ListOptionsAll,
		RepositoryID:      repo.ID,
		UserID:            userFilter,
		CreatedAfterUnix:  from.Unix(),
		CreatedBeforeUnix: to.AddDate(0, 0, 1).Add(-time.Second).Unix(),
	}
	times, err := issues_model.GetTrackedTimes(ctx, opts)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	times, truncated := planning_service.Truncate(times, MaxTimesheetEntries)

	running, err := timesheetRunningStopwatches(ctx, repo.ID)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if userFilter > 0 {
		filtered := make([]timesheetStopwatch, 0, len(running))
		for _, r := range running {
			if r.UserID == userFilter {
				filtered = append(filtered, r)
			}
		}
		running = filtered
	}

	issueIDs := make([]int64, 0, len(times)+len(running))
	seenIssue := map[int64]bool{}
	for _, t := range times {
		if !seenIssue[t.IssueID] {
			seenIssue[t.IssueID] = true
			issueIDs = append(issueIDs, t.IssueID)
		}
	}
	for _, r := range running {
		if !seenIssue[r.IssueID] {
			seenIssue[r.IssueID] = true
			issueIDs = append(issueIDs, r.IssueID)
		}
	}
	issues, err := issues_model.GetIssuesByIDs(ctx, issueIDs)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	issuesByID := make(map[int64]*issues_model.Issue, len(issues))
	for _, iss := range issues {
		issuesByID[iss.ID] = iss
	}

	assignments, err := planning_service.Assignments(ctx, issueIDs)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	milestones, err := db.Find[issues_model.Milestone](ctx, issues_model.FindMilestoneOptions{RepoID: repo.ID})
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	milestoneTitles := make(map[int64]string, len(milestones))
	for _, m := range milestones {
		milestoneTitles[m.ID] = m.Name
	}

	assignees, err := repo_model.GetRepoAssignees(ctx, repo)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	laneUserIDs := userIDsOf(assignees)
	if userFilter > 0 {
		laneUserIDs = []int64{userFilter}
	}
	laneSeen := make(map[int64]bool, len(laneUserIDs))
	for _, id := range laneUserIDs {
		laneSeen[id] = true
	}
	for _, t := range times {
		if !laneSeen[t.UserID] {
			laneSeen[t.UserID] = true
			laneUserIDs = append(laneUserIDs, t.UserID)
		}
	}
	slices.Sort(laneUserIDs)

	userIDs := append([]int64{}, laneUserIDs...)
	for _, r := range running {
		if !laneSeen[r.UserID] {
			userIDs = append(userIDs, r.UserID)
			laneSeen[r.UserID] = true
		}
	}
	usersByID, err := user_model.GetUsersMapByIDs(ctx, userIDs)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	userFor := func(id int64) *user_model.User {
		if u := usersByID[id]; u != nil {
			return u
		}
		return user_model.NewGhostUser()
	}

	byUserDay := map[[2]int64][]*issues_model.TrackedTime{}
	for _, t := range times {
		day := time.Unix(t.CreatedUnix, 0).UTC().Truncate(24 * time.Hour).Unix()
		key := [2]int64{t.UserID, day}
		byUserDay[key] = append(byUserDay[key], t)
	}

	lanes := make([]TimesheetLane, 0, len(laneUserIDs))
	for _, uid := range laneUserIDs {
		u := userFor(uid)
		days := make([]TimesheetDay, 0, maxTimesheetWindowDays)
		var laneTotal int64
		for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
			group := byUserDay[[2]int64{uid, day.Unix()}]
			sort.Slice(group, func(i, j int) bool {
				if group[i].CreatedUnix != group[j].CreatedUnix {
					return group[i].CreatedUnix < group[j].CreatedUnix
				}
				return group[i].ID < group[j].ID
			})
			entries := make([]TimesheetEntry, 0, len(group))
			var daySeconds int64
			for _, t := range group {
				iss := issuesByID[t.IssueID]
				var number int64
				var title string
				if iss != nil {
					number, title = iss.Index, iss.Title
				}
				entries = append(entries, TimesheetEntry{
					ID: t.ID, IssueID: t.IssueID, Number: number, Title: title,
					Seconds: t.Time, CreatedUnix: t.CreatedUnix,
					Editable: t.UserID == ctx.Doer.ID || canWriteIssues,
				})
				daySeconds += t.Time
			}
			days = append(days, TimesheetDay{Unix: day.Unix(), Seconds: daySeconds, Entries: entries})
			laneTotal += daySeconds
		}
		lanes = append(lanes, TimesheetLane{
			UserID: uid, Login: u.Name, DisplayName: u.DisplayName(), AvatarURL: u.AvatarLink(ctx),
			Days: days, TotalSeconds: laneTotal,
		})
	}

	runningOut := make([]TimesheetRow, 0, len(running))
	for _, r := range running {
		iss := issuesByID[r.IssueID]
		var number int64
		var title string
		if iss != nil {
			number, title = iss.Index, iss.Title
		}
		runningOut = append(runningOut, TimesheetRow{
			UserID: r.UserID, Login: userFor(r.UserID).Name, IssueID: r.IssueID,
			Number: number, Title: title, StartedUnix: r.CreatedUnix,
		})
	}
	sort.Slice(runningOut, func(i, j int) bool {
		if runningOut[i].UserID != runningOut[j].UserID {
			return runningOut[i].UserID < runningOut[j].UserID
		}
		return runningOut[i].IssueID < runningOut[j].IssueID
	})

	issueTotals := map[int64]*TimesheetIssueTotal{}
	userTotals := map[int64]*TimesheetUserTotal{}
	milestoneTotals := map[int64]*TimesheetMilestoneTotal{}
	typeTotals := map[int64]*TimesheetTypeTotal{}
	for _, t := range times {
		iss := issuesByID[t.IssueID]

		if it, ok := issueTotals[t.IssueID]; ok {
			it.Seconds += t.Time
		} else {
			var number int64
			var title string
			if iss != nil {
				number, title = iss.Index, iss.Title
			}
			issueTotals[t.IssueID] = &TimesheetIssueTotal{IssueID: t.IssueID, Number: number, Title: title, Seconds: t.Time}
		}

		if ut, ok := userTotals[t.UserID]; ok {
			ut.Seconds += t.Time
		} else {
			userTotals[t.UserID] = &TimesheetUserTotal{UserID: t.UserID, Login: userFor(t.UserID).Name, Seconds: t.Time}
		}

		if iss != nil && iss.MilestoneID > 0 {
			if mt, ok := milestoneTotals[iss.MilestoneID]; ok {
				mt.Seconds += t.Time
			} else {
				milestoneTotals[iss.MilestoneID] = &TimesheetMilestoneTotal{
					MilestoneID: iss.MilestoneID, Title: milestoneTitles[iss.MilestoneID], Seconds: t.Time,
				}
			}
		}

		if at, ok := assignments[t.IssueID]; ok {
			if tt, ok := typeTotals[at.TypeID]; ok {
				tt.Seconds += t.Time
			} else {
				typeTotals[at.TypeID] = &TimesheetTypeTotal{TypeID: at.TypeID, Name: at.Name, Seconds: t.Time}
			}
		}
	}

	out := &Timesheet{
		RepoID: repo.ID, FromUnix: from.Unix(), ToUnix: to.Unix(),
		Lanes: lanes, Running: runningOut, Truncated: truncated,
		Totals: TimesheetTotals{
			ByIssue:     sortedIssueTotals(issueTotals),
			ByUser:      sortedUserTotals(userTotals),
			ByMilestone: sortedMilestoneTotals(milestoneTotals),
			ByType:      sortedTypeTotals(typeTotals),
		},
	}
	ctx.JSON(http.StatusOK, out)
}

func sortedIssueTotals(m map[int64]*TimesheetIssueTotal) []TimesheetIssueTotal {
	out := make([]TimesheetIssueTotal, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IssueID < out[j].IssueID })
	return out
}

func sortedUserTotals(m map[int64]*TimesheetUserTotal) []TimesheetUserTotal {
	out := make([]TimesheetUserTotal, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out
}

func sortedMilestoneTotals(m map[int64]*TimesheetMilestoneTotal) []TimesheetMilestoneTotal {
	out := make([]TimesheetMilestoneTotal, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MilestoneID < out[j].MilestoneID })
	return out
}

func sortedTypeTotals(m map[int64]*TimesheetTypeTotal) []TimesheetTypeTotal {
	out := make([]TimesheetTypeTotal, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TypeID < out[j].TypeID })
	return out
}
