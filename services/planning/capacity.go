// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"context"
	"net/http"
	"slices"
	"time"

	hub_model "gitea.dev/models/hub"
	org_model "gitea.dev/models/organization"
	access_model "gitea.dev/models/perm/access"
	planning_model "gitea.dev/models/planning"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	user_model "gitea.dev/models/user"
)

// The four sources a resolved capacity can come from, nearest scope first.
const (
	CapacitySourceRepo     = "repo"
	CapacitySourceOrg      = "org"
	CapacitySourceInstance = "instance"
	CapacitySourceDefault  = "default"
)

// The default a user with no recorded row anywhere resolves to: an eight-hour day at 80%
// utilization, Monday through Friday.
const (
	DefaultHoursPerDay = 8.0
	DefaultUtilization = 0.8
	DefaultWorkdays    = 62
)

// Capacity is one user's resolved capacity: repo over org over instance over default.
type Capacity struct {
	HoursPerDay float64 `json:"hours_per_day"`
	Utilization float64 `json:"utilization"`
	Workdays    int     `json:"workdays"`
	Source      string  `json:"source"`
}

// ResolveCapacity reads every userID's nearest-scope capacity as seen from repo: the
// repository's own row, its organization's, the instance's, or the default.
func ResolveCapacity(ctx context.Context, repo *repo_model.Repository, userIDs []int64) (map[int64]Capacity, error) {
	orgID, err := repoOrgID(ctx, repo)
	if err != nil {
		return nil, err
	}
	return resolveCapacityIn(ctx, repo.ID, orgID, userIDs)
}

// ResolveCapacityForOrg is ResolveCapacity's counterpart for an organization scope: its own
// row, the instance's, or the default. orgID 0 resolves the instance scope alone.
func ResolveCapacityForOrg(ctx context.Context, orgID int64, userIDs []int64) (map[int64]Capacity, error) {
	return resolveCapacityIn(ctx, 0, orgID, userIDs)
}

func resolveCapacityIn(ctx context.Context, repoID, orgID int64, userIDs []int64) (map[int64]Capacity, error) {
	rows, err := planning_model.CapacitiesFor(ctx, userIDs, repoID, orgID)
	if err != nil {
		return nil, err
	}
	return shadowCapacities(rows, repoID, orgID, userIDs), nil
}

// shadowCapacities keeps, for each user, the nearest-scope row — repo over org over instance —
// and answers the published default for a user with no row at all.
func shadowCapacities(rows []*planning_model.UserCapacity, repoID, orgID int64, userIDs []int64) map[int64]Capacity {
	nearness := func(row *planning_model.UserCapacity) int {
		switch {
		case repoID > 0 && row.RepoID == repoID:
			return 0
		case orgID > 0 && row.OrgID == orgID:
			return 1
		default:
			return 2
		}
	}
	best := map[int64]*planning_model.UserCapacity{}
	for _, row := range rows {
		if cur, ok := best[row.UserID]; !ok || nearness(row) < nearness(cur) {
			best[row.UserID] = row
		}
	}
	out := make(map[int64]Capacity, len(userIDs))
	for _, uid := range userIDs {
		row, ok := best[uid]
		if !ok {
			out[uid] = Capacity{HoursPerDay: DefaultHoursPerDay, Utilization: DefaultUtilization, Workdays: DefaultWorkdays, Source: CapacitySourceDefault}
			continue
		}
		source := CapacitySourceInstance
		switch {
		case row.RepoID > 0:
			source = CapacitySourceRepo
		case row.OrgID > 0:
			source = CapacitySourceOrg
		}
		out[uid] = Capacity{HoursPerDay: row.HoursPerDay, Utilization: row.Utilization, Workdays: row.Workdays, Source: source}
	}
	return out
}

var errBadHours = &hub_model.Error{
	Code: "bad_hours", Status: http.StatusUnprocessableEntity,
	Message:         "hours_per_day must be greater than 0 and at most 24",
	SuggestedAction: "Send a value in (0, 24].",
}

var errBadUtilization = &hub_model.Error{
	Code: "bad_utilization", Status: http.StatusUnprocessableEntity,
	Message:         "utilization must be greater than 0 and at most 1",
	SuggestedAction: "Send a fraction in (0, 1], such as 0.8 for 80%.",
}

var errBadWorkdays = &hub_model.Error{
	Code: "bad_workdays", Status: http.StatusUnprocessableEntity,
	Message:         "workdays must be a bit mask between 1 and 127",
	SuggestedAction: "Send a value from 1 to 127: a bit per day of the week, Sunday as bit 0.",
}

// ValidateCapacity checks a capacity write's three numbers, in the order a caller can fix
// them one at a time.
func ValidateCapacity(hoursPerDay, utilization float64, workdays int) error {
	if hoursPerDay <= 0 || hoursPerDay > 24 {
		return errBadHours
	}
	if utilization <= 0 || utilization > 1 {
		return errBadUtilization
	}
	if workdays < 1 || workdays > 127 {
		return errBadWorkdays
	}
	return nil
}

var errCapacityForbidden = &hub_model.Error{
	Code: "forbidden", Status: http.StatusForbidden,
	Message:         "you may only set your own capacity, or that of a scope you administer",
	SuggestedAction: "Ask a repository administrator, an organization owner, or a site administrator to make this change.",
}

var errCapacityUserNotFound = &hub_model.Error{
	Code: "not_found", Status: http.StatusNotFound,
	Message:         "no user with that id exists",
	SuggestedAction: "Check the id against the repository's or organization's own member list.",
}

var errCapacityScopeNotFound = &hub_model.Error{
	Code: "not_found", Status: http.StatusNotFound,
	Message:         "no repository or organization with that id is visible to you",
	SuggestedAction: "Check the id against the repositories or organizations you can see.",
}

var errCapacityNotAssignee = &hub_model.Error{
	Code: "not_an_assignee", Status: http.StatusUnprocessableEntity,
	Message:         "the target user is not an assignee of this repository or a member of this organization",
	SuggestedAction: "Set capacity only for a user this scope already involves.",
}

// resolveCapacityScope confirms scope's own target exists and is visible to doer, before any
// write runs: a repository or organization invisible to doer answers exactly like one that does
// not exist, so a capacity write never discloses which. Both zero is the instance scope, always
// resolved.
func resolveCapacityScope(ctx context.Context, doer *user_model.User, scope Scope) error {
	switch {
	case scope.RepoID > 0:
		repo, err := repo_model.GetRepositoryByID(ctx, scope.RepoID)
		if err != nil {
			return errCapacityScopeNotFound
		}
		perm, err := access_model.GetDoerRepoPermission(ctx, repo, doer)
		if err != nil {
			return err
		}
		if !perm.CanRead(unit.TypeIssues) {
			return errCapacityScopeNotFound
		}
	case scope.OrgID > 0:
		org, err := org_model.GetOrgByID(ctx, scope.OrgID)
		if err != nil {
			return errCapacityScopeNotFound
		}
		if !org_model.HasOrgOrUserVisible(ctx, org.AsUser(), doer) {
			return errCapacityScopeNotFound
		}
	}
	return nil
}

// capacityScopeError refuses a scope naming both a repository and an organization: unlike a
// field or a type, a capacity row's instance scope is not admin-only, since a user is always
// free to declare their own default.
func capacityScopeError() error {
	return &hub_model.Error{
		Code: "bad_scope", Status: http.StatusUnprocessableEntity,
		Message:         "a capacity row cannot be scoped to both a repository and an organization",
		SuggestedAction: "Send repo_id or org_id, never both.",
	}
}

// capacityWriteCheck refuses a write targetUserID may not make: the doer may always set their
// own row; setting another's needs the scope's own administrator AND, for a repo or org scope,
// that target actually belongs to it — a repository assignee or an organization member — since
// an administrator sets capacity for people the scope involves, not an arbitrary user id. The
// instance scope carries no such membership, so a site administrator may target anyone.
func capacityWriteCheck(ctx context.Context, doer *user_model.User, targetUserID int64, scope Scope) error {
	if doer.ID == targetUserID {
		return nil
	}
	ok, err := scopeAdmin(ctx, doer, scope)
	if err != nil {
		return err
	}
	if !ok {
		return errCapacityForbidden
	}
	switch {
	case scope.RepoID > 0:
		repo, err := repo_model.GetRepositoryByID(ctx, scope.RepoID)
		if err != nil {
			return err
		}
		assignees, err := repo_model.GetRepoAssignees(ctx, repo)
		if err != nil {
			return err
		}
		for _, u := range assignees {
			if u.ID == targetUserID {
				return nil
			}
		}
		return errCapacityNotAssignee
	case scope.OrgID > 0:
		org, err := org_model.GetOrgByID(ctx, scope.OrgID)
		if err != nil {
			return err
		}
		isMember, err := org.IsOrgMember(ctx, targetUserID)
		if err != nil {
			return err
		}
		if !isMember {
			return errCapacityNotAssignee
		}
	}
	return nil
}

// resolveAfterWrite re-resolves targetUserID's capacity in scope straight from storage, so a
// set or a clear replies with exactly what a following read would see.
func resolveAfterWrite(ctx context.Context, targetUserID int64, scope Scope) (Capacity, error) {
	var (
		caps map[int64]Capacity
		err  error
	)
	if scope.RepoID > 0 {
		repo, repoErr := repo_model.GetRepositoryByID(ctx, scope.RepoID)
		if repoErr != nil {
			return Capacity{}, repoErr
		}
		caps, err = ResolveCapacity(ctx, repo, []int64{targetUserID})
	} else {
		caps, err = ResolveCapacityForOrg(ctx, scope.OrgID, []int64{targetUserID})
	}
	if err != nil {
		return Capacity{}, err
	}
	return caps[targetUserID], nil
}

// SetCapacity records targetUserID's capacity in scope, refusing a bad scope, a caller who is
// neither the target nor the scope's administrator, a target user that does not exist, or a
// bad number — every check runs before the row is written.
func SetCapacity(ctx context.Context, doer *user_model.User, targetUserID int64, scope Scope, hoursPerDay, utilization float64, workdays int) (Capacity, error) {
	if scope.RepoID > 0 && scope.OrgID > 0 {
		return Capacity{}, capacityScopeError()
	}
	if err := resolveCapacityScope(ctx, doer, scope); err != nil {
		return Capacity{}, err
	}
	if err := capacityWriteCheck(ctx, doer, targetUserID, scope); err != nil {
		return Capacity{}, err
	}
	if _, err := user_model.GetUserByID(ctx, targetUserID); err != nil {
		return Capacity{}, errCapacityUserNotFound
	}
	if err := ValidateCapacity(hoursPerDay, utilization, workdays); err != nil {
		return Capacity{}, err
	}
	row := &planning_model.UserCapacity{
		UserID: targetUserID, RepoID: scope.RepoID, OrgID: scope.OrgID,
		HoursPerDay: hoursPerDay, Utilization: utilization, Workdays: workdays,
	}
	if err := planning_model.UpsertUserCapacity(ctx, row); err != nil {
		return Capacity{}, err
	}
	return resolveAfterWrite(ctx, targetUserID, scope)
}

// ClearCapacity removes targetUserID's own row in scope, if any, under the same self-or-admin
// check SetCapacity applies, and replies with what the scope now resolves to.
func ClearCapacity(ctx context.Context, doer *user_model.User, targetUserID int64, scope Scope) (Capacity, error) {
	if scope.RepoID > 0 && scope.OrgID > 0 {
		return Capacity{}, capacityScopeError()
	}
	if err := resolveCapacityScope(ctx, doer, scope); err != nil {
		return Capacity{}, err
	}
	if err := capacityWriteCheck(ctx, doer, targetUserID, scope); err != nil {
		return Capacity{}, err
	}
	if err := planning_model.DeleteUserCapacity(ctx, targetUserID, scope.RepoID, scope.OrgID); err != nil {
		return Capacity{}, err
	}
	return resolveAfterWrite(ctx, targetUserID, scope)
}

// LoadItem is one issue's remaining work assigned to one user: a share (max(estimate -
// tracked, 0) divided by the issue's own assignee count) of RemainingSeconds spread over the
// issue's own window [StartUnix, EndUnix]. Points is that same share of the issue's points, so
// SprintLoad's own sum never double-counts a multi-assignee issue.
type LoadItem struct {
	IssueID          int64
	Number           int64
	Title            string
	UserID           int64
	MilestoneID      int64
	RemainingSeconds int64
	Points           float64
	StartUnix        int64
	EndUnix          int64
}

// UnestimatedIssue is one item SpreadLoad could not place on the heat strip: it carries no
// load because its remaining work resolved to zero.
type UnestimatedIssue struct {
	IssueID int64  `json:"issue_id"`
	Number  int64  `json:"number"`
	Title   string `json:"title"`
}

// DayLoad is one lane's one day: the load spread onto it, the hours the day's capacity makes
// available, and whether the first exceeds the second.
type DayLoad struct {
	Unix           int64   `json:"unix"`
	LoadHours      float64 `json:"load_hours"`
	AvailableHours float64 `json:"available_hours"`
	Over           bool    `json:"over"`
}

// Lane is one user's heat strip over [from, to]: a day-by-day series plus the issues that
// carried no load at all.
type Lane struct {
	UserID              int64
	Days                []DayLoad
	Unestimated         []UnestimatedIssue
	TotalLoadHours      float64
	TotalAvailableHours float64
	Over                bool
}

// WorkingDays lists the UTC calendar days from start to end inclusive whose weekday bit is
// set in mask — Sunday is bit 0, matching time.Weekday directly, so mask&(1<<d.Weekday())
// reads a day's own membership with no lookup table. A mask matching no day in the window
// falls back to the start day alone, so a caller always has at least one day to spread load
// over rather than dividing by zero.
func WorkingDays(start, end time.Time, mask int) []time.Time {
	start = start.UTC().Truncate(24 * time.Hour)
	end = end.UTC().Truncate(24 * time.Hour)
	if end.Before(start) {
		start, end = end, start
	}
	days := make([]time.Time, 0, 8)
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if mask&(1<<uint(d.Weekday())) != 0 {
			days = append(days, d)
		}
	}
	if len(days) == 0 {
		return []time.Time{start}
	}
	return days
}

// SpreadLoad folds items onto one lane per user in caps — every capacity-bearing user gets a
// lane, even one with no item at all — spreading each item's remaining hours evenly over its
// own working days (per that user's own workdays mask) and clipping the result to [from, to].
// A day's available hours are hours-per-day times utilization on that user's own working days,
// zero on every other day, so a load landing outside them always reads as over.
func SpreadLoad(items []LoadItem, caps map[int64]Capacity, from, to time.Time) []Lane {
	from = from.UTC().Truncate(24 * time.Hour)
	to = to.UTC().Truncate(24 * time.Hour)

	userIDs := make([]int64, 0, len(caps))
	for uid := range caps {
		userIDs = append(userIDs, uid)
	}
	slices.Sort(userIDs)

	loadByUserDay := map[int64]map[int64]float64{}
	unestimated := map[int64][]UnestimatedIssue{}
	for _, item := range items {
		itemCap, ok := caps[item.UserID]
		if !ok {
			continue
		}
		if item.RemainingSeconds <= 0 {
			unestimated[item.UserID] = append(unestimated[item.UserID], UnestimatedIssue{IssueID: item.IssueID, Number: item.Number, Title: item.Title})
			continue
		}
		workDays := WorkingDays(time.Unix(item.StartUnix, 0), time.Unix(item.EndUnix, 0), itemCap.Workdays)
		perDay := float64(item.RemainingSeconds) / 3600 / float64(len(workDays))
		for _, d := range workDays {
			if d.Before(from) || d.After(to) {
				continue
			}
			if loadByUserDay[item.UserID] == nil {
				loadByUserDay[item.UserID] = map[int64]float64{}
			}
			loadByUserDay[item.UserID][d.Unix()] += perDay
		}
	}

	lanes := make([]Lane, 0, len(userIDs))
	for _, uid := range userIDs {
		c := caps[uid]
		lane := Lane{UserID: uid, Days: make([]DayLoad, 0, 8), Unestimated: unestimated[uid]}
		if lane.Unestimated == nil {
			lane.Unestimated = []UnestimatedIssue{}
		}
		for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
			load := loadByUserDay[uid][d.Unix()]
			available := 0.0
			if c.Workdays&(1<<uint(d.Weekday())) != 0 {
				available = c.HoursPerDay * c.Utilization
			}
			over := load > available
			lane.Days = append(lane.Days, DayLoad{Unix: d.Unix(), LoadHours: load, AvailableHours: available, Over: over})
			lane.TotalLoadHours += load
			lane.TotalAvailableHours += available
			if over {
				lane.Over = true
			}
		}
		lanes = append(lanes, lane)
	}
	return lanes
}

// Sprint is one milestone window a sprint lane is folded over.
type Sprint struct {
	MilestoneID int64
	Title       string
	StartUnix   int64
	EndUnix     int64
}

// SprintLane is one user's whole-sprint total: every item whose milestone falls in the
// sprint's window, folded to one number rather than spread day by day.
type SprintLane struct {
	UserID         int64   `json:"user_id"`
	LoadHours      float64 `json:"load_hours"`
	AvailableHours float64 `json:"available_hours"`
	Over           bool    `json:"over"`
	Points         float64 `json:"points"`
}

// SprintRow is one sprint's own row: its window, its working-day count, and one lane per user
// who carries load in it.
type SprintRow struct {
	MilestoneID int64
	Title       string
	StartUnix   int64
	EndUnix     int64
	WorkingDays int
	Lanes       []SprintLane
}

// SprintLoad folds items into one row per sprint: an item falls in the sprint whose
// MilestoneID matches its own — never by date containment, since an item's own bar window can
// sit outside its milestone's window entirely — and contributes its WHOLE remaining hours there
// rather than the day-by-day spread SpreadLoad computes — a sprint total is a commitment against
// the sprint, not a heat strip. An item with no milestone (MilestoneID zero) loads no sprint.
// Available is working days (by that user's own mask) times hours-per-day times utilization.
func SprintLoad(items []LoadItem, caps map[int64]Capacity, sprints []Sprint) []SprintRow {
	rows := make([]SprintRow, 0, len(sprints))
	for _, sprint := range sprints {
		start, end := time.Unix(sprint.StartUnix, 0), time.Unix(sprint.EndUnix, 0)
		byUser := map[int64]*SprintLane{}
		userIDs := make([]int64, 0, 4)
		for _, item := range items {
			if item.MilestoneID == 0 || item.MilestoneID != sprint.MilestoneID {
				continue
			}
			c, ok := caps[item.UserID]
			if !ok {
				continue
			}
			lane, ok := byUser[item.UserID]
			if !ok {
				workDays := len(WorkingDays(start, end, c.Workdays))
				lane = &SprintLane{UserID: item.UserID, AvailableHours: float64(workDays) * c.HoursPerDay * c.Utilization}
				byUser[item.UserID] = lane
				userIDs = append(userIDs, item.UserID)
			}
			lane.LoadHours += float64(item.RemainingSeconds) / 3600
			lane.Points += item.Points
		}
		slices.Sort(userIDs)
		lanes := make([]SprintLane, 0, len(userIDs))
		for _, uid := range userIDs {
			lane := byUser[uid]
			lane.Over = lane.LoadHours > lane.AvailableHours
			lanes = append(lanes, *lane)
		}
		rows = append(rows, SprintRow{
			MilestoneID: sprint.MilestoneID, Title: sprint.Title,
			StartUnix: sprint.StartUnix, EndUnix: sprint.EndUnix,
			WorkingDays: len(WorkingDays(start, end, DefaultWorkdays)),
			Lanes:       lanes,
		})
	}
	return rows
}
