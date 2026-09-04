// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/json"
	"gitea.dev/modules/optional"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	"gitea.dev/services/hub/query"
	planning_service "gitea.dev/services/planning"
)

// The roadmap's per-user capacity: heat strips over a day-by-day window and per-sprint totals,
// modelled on Azure DevOps sprint capacity. maxCapacityWindowDays bounds the heat strip so a
// caller cannot ask for a chart that never finishes rendering; maxCapacityIssues bounds how
// many of a repository's open assigned issues are folded in, the same truncation contract
// GetRoadmap's own page limit carries.
const (
	maxCapacityWindowDays = 366
	maxCapacityIssues     = 2000
)

// CapacityRow is one user's resolved capacity, published by GET /capacity.
type CapacityRow struct {
	UserID      int64   `json:"user_id"`
	Login       string  `json:"login"`
	DisplayName string  `json:"display_name"`
	AvatarURL   string  `json:"avatar_url"`
	HoursPerDay float64 `json:"hours_per_day"`
	Utilization float64 `json:"utilization"`
	Workdays    int     `json:"workdays"`
	Source      string  `json:"source"`
}

// RoadmapCapacity is GET /roadmap/capacity's response: one heat-strip lane per repository
// assignee plus one row per schedulable sprint.
type RoadmapCapacity struct {
	Lanes              []RoadmapCapacityLane              `json:"lanes"`
	Sprints            []RoadmapCapacitySprint            `json:"sprints"`
	SprintsUnscheduled []RoadmapCapacitySprintUnscheduled `json:"sprints_unscheduled"`
	Truncated          bool                               `json:"truncated"`
}

// RoadmapCapacityLane is one assignee's heat strip: their own resolved capacity, their day-by-
// day load over the window, and the issues that carried none because their remaining work is
// zero.
type RoadmapCapacityLane struct {
	UserID              int64                               `json:"user_id"`
	Login               string                              `json:"login"`
	DisplayName         string                              `json:"display_name"`
	AvatarURL           string                              `json:"avatar_url"`
	HoursPerDay         float64                             `json:"hours_per_day"`
	Utilization         float64                             `json:"utilization"`
	Workdays            int                                 `json:"workdays"`
	TotalLoadHours      float64                             `json:"total_load_hours"`
	TotalAvailableHours float64                             `json:"total_available_hours"`
	Over                bool                                `json:"over"`
	Days                []planning_service.DayLoad          `json:"days"`
	Unestimated         []planning_service.UnestimatedIssue `json:"unestimated"`
}

// RoadmapCapacitySprint is one schedulable milestone's own sprint row.
type RoadmapCapacitySprint struct {
	MilestoneID int64                       `json:"milestone_id"`
	Title       string                      `json:"title"`
	StartUnix   int64                       `json:"start_unix"`
	EndUnix     int64                       `json:"end_unix"`
	WorkingDays int                         `json:"working_days"`
	Lanes       []RoadmapCapacitySprintLane `json:"lanes"`
}

// RoadmapCapacitySprintLane is one user's whole-sprint total.
type RoadmapCapacitySprintLane struct {
	UserID         int64   `json:"user_id"`
	Login          string  `json:"login"`
	LoadHours      float64 `json:"load_hours"`
	AvailableHours float64 `json:"available_hours"`
	Over           bool    `json:"over"`
	Points         float64 `json:"points"`
}

// RoadmapCapacitySprintUnscheduled is a milestone SprintLoad could not fold in: it carries
// neither a recorded start nor a due date, or only one of the two.
type RoadmapCapacitySprintUnscheduled struct {
	MilestoneID int64    `json:"milestone_id"`
	Title       string   `json:"title"`
	Missing     []string `json:"missing"`
}

var roadmapCapacitySpec = query.Spec{
	Resource: "roadmap-capacity",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "from", Column: "from", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
		{Name: "to", Column: "to", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
	},
	PrimaryKey: "user_id",
}

var capacitySpec = query.Spec{
	Resource: "capacity",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "org_id", Column: "org_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
	},
	PrimaryKey: "user_id",
}

var capacityUserIDParam = []hubapi.Param{
	{Name: "user_id", In: "path", Type: "integer", Required: true, Description: "The user's id."},
}

// capacityScopeParams is the pair every capacity write names its scope with — a repository, an
// organization, or, both zero, the whole instance.
var capacityScopeParams = []hubapi.Param{
	{Name: "repo_id", In: "body", Type: "integer", Description: "Scope to this repository. Mutually exclusive with org_id; both zero is the instance scope."},
	{Name: "org_id", In: "body", Type: "integer", Description: "Scope to this organization. Mutually exclusive with repo_id."},
}

var capacitySetBodyParams = append(append([]hubapi.Param{}, capacityScopeParams...),
	hubapi.Param{Name: "hours_per_day", In: "body", Type: "number", Required: true, Description: "A number in (0, 24], such as 8 or 6.5. Accepted as either a JSON number or a numeric string."},
	hubapi.Param{Name: "utilization", In: "body", Type: "number", Required: true, Description: "A fraction in (0, 1], such as 0.8 for 80%. Accepted as either a JSON number or a numeric string."},
	hubapi.Param{Name: "workdays", In: "body", Type: "integer", Required: true, Description: "A bit mask from 1 to 127: a bit per day of the week, Sunday as bit 0. 62 is Monday through Friday."},
)

func getRoadmapCapacityEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getRoadmapCapacity", Method: http.MethodGet, Path: "/roadmap/capacity",
			Summary: "Per-user capacity heat strips and sprint load for the roadmap",
			Description: "One lane per user GET /repos/{owner}/{repo} would list as an assignee, even one carrying no " +
				"load, over assigned open issues only. Each issue's remaining work — max(estimate - tracked, 0), read " +
				"from Gitea's own time-tracking — is split evenly across its own assignees and spread over its working " +
				"days by that user's own resolved workdays mask; a day is over when its load exceeds hours-per-day times " +
				"utilization. from and to are YYYY-MM-DD dates, at most 366 days apart (bad_window otherwise); omitted, " +
				"they default to the window this repository's own bars cover. sprints folds the same remaining work, " +
				"whole per issue rather than day by day, into every milestone that carries both a recorded start and a " +
				"due date; one still missing either is listed in sprints_unscheduled instead. truncated marks a " +
				"repository with more than 2000 open assigned issues, where only a prefix was considered. Scoped by " +
				"Gitea's own permission check on the Issues unit.",
			Tag: "capacity", Query: &roadmapCapacitySpec, Response: "RoadmapCapacity", ResponseIs: "object",
			CLINames: []string{"roadmap-capacity"},
		},
		Handler: GetRoadmapCapacity,
	}
}

func getCapacityEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getCapacity", Method: http.MethodGet, Path: "/capacity",
			Summary: "Resolved capacity for a scope's own users",
			Description: "repo_id reads every assignee GET /repos/{owner}/{repo} would list, org_id every member. Each " +
				"row's capacity is resolved nearest scope first — repo, then org, then instance, then the published " +
				"default — and source names which one answered. Readable the same way GET /issue-types checks its scope.",
			Tag: "capacity", Query: &capacitySpec, Response: "CapacityRow", ResponseIs: "array",
			CLINames: []string{"capacity"},
		},
		Handler: GetCapacity,
	}
}

func setCapacityEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "setCapacity", Method: http.MethodPut, Path: "/capacity/{user_id}",
			Summary: "Set a user's capacity in a scope",
			Description: "The doer may always set their own row; setting another user's needs the scope's own " +
				"administrator — a repository administrator, an organization owner, or, for the instance scope, a site " +
				"administrator. Refused bad_scope, forbidden, not_found (no such user), bad_hours, bad_utilization or " +
				"bad_workdays. Replies with the row this scope now resolves to.",
			Tag: "capacity", PathParams: capacityUserIDParam, Body: capacitySetBodyParams,
			CLINames: []string{"capacity-set"},
			Response: "Capacity", ResponseIs: "object",
		},
		Handler: SetCapacity,
	}
}

func clearCapacityEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "clearCapacity", Method: http.MethodDelete, Path: "/capacity/{user_id}",
			Summary:     "Clear a user's own row in a scope",
			Description: "Same self-or-administrator check as setting one. Replies with what the scope now resolves to.",
			Tag:         "capacity", PathParams: capacityUserIDParam, Body: capacityScopeParams,
			CLINames: []string{"capacity-clear"},
			Response: "Capacity", ResponseIs: "object",
		},
		Handler: ClearCapacity,
	}
}

// capacityRowFrom pairs a user with their resolved capacity.
func capacityRowFrom(ctx *context.APIContext, u *user_model.User, c planning_service.Capacity) CapacityRow {
	return CapacityRow{
		UserID: u.ID, Login: u.Name, DisplayName: u.DisplayName(), AvatarURL: u.AvatarLink(ctx),
		HoursPerDay: c.HoursPerDay, Utilization: c.Utilization, Workdays: c.Workdays, Source: c.Source,
	}
}

func userIDsOf(users []*user_model.User) []int64 {
	ids := make([]int64, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	return ids
}

// GetCapacity answers GET /capacity.
func GetCapacity(ctx *context.APIContext) {
	q, ok := hubapi.ParseQuery(ctx, capacitySpec)
	if !ok {
		return
	}
	repoID := hubapi.EqualityFilterInt(q, "repo_id")
	orgID := hubapi.EqualityFilterInt(q, "org_id")
	if repoID > 0 && orgID > 0 {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_scope",
			"repo_id and org_id cannot both be given", "Send repo_id or org_id, never both.")
		return
	}

	var (
		users []*user_model.User
		caps  map[int64]planning_service.Capacity
		err   error
	)
	switch {
	case repoID > 0:
		repo, ok := issueTypeReadableRepo(ctx, repoID)
		if !ok {
			return
		}
		if users, err = repo_model.GetRepoAssignees(ctx, repo); err != nil {
			ctx.APIErrorInternal(err)
			return
		}
		if caps, err = planning_service.ResolveCapacity(ctx, repo, userIDsOf(users)); err != nil {
			ctx.APIErrorInternal(err)
			return
		}
	case orgID > 0:
		org, ok := issueTypeVisibleOrg(ctx, orgID)
		if !ok {
			return
		}
		if users, _, err = org.GetMembers(ctx, ctx.Doer); err != nil {
			ctx.APIErrorInternal(err)
			return
		}
		if caps, err = planning_service.ResolveCapacityForOrg(ctx, orgID, userIDsOf(users)); err != nil {
			ctx.APIErrorInternal(err)
			return
		}
	default:
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "missing_scope",
			"repo_id or org_id is required", "Pass ?repo_id=<id> or ?org_id=<id>.")
		return
	}

	rows := make([]CapacityRow, 0, len(users))
	for _, u := range users {
		rows = append(rows, capacityRowFrom(ctx, u, caps[u.ID]))
	}
	ctx.JSON(http.StatusOK, rows)
}

// capacityBody is the shape PUT and DELETE /capacity/{user_id} share. HoursPerDay and
// Utilization come off the wire as either a JSON number or a numeric string, since the CLI's
// own generator has no float body type and sends every non-integer member as a string.
type capacityBody struct {
	RepoID      int64 `json:"repo_id"`
	OrgID       int64 `json:"org_id"`
	HoursPerDay any   `json:"hours_per_day"`
	Utilization any   `json:"utilization"`
	Workdays    int   `json:"workdays"`
}

func readCapacityBody(ctx *context.APIContext) (*capacityBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(ctx.Req.Body, maxWriteBody+1))
	if err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "unreadable_body",
			"the request body could not be read", "Retry the request.")
		return nil, false
	}
	if len(raw) > maxWriteBody {
		hubapi.APIError(ctx, http.StatusBadRequest, "body_too_large",
			"the request body is larger than 16KiB", "A capacity row is a handful of short fields; send only those.")
		return nil, false
	}
	body := new(capacityBody)
	if err := json.Unmarshal(raw, body); err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"repo_id": 1, "hours_per_day": 8, "utilization": 0.8, "workdays": 62}.`)
		return nil, false
	}
	return body, true
}

// parseCapacityNumber reads hours_per_day or utilization: a JSON number, or a numeric string.
func parseCapacityNumber(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

var errCapacityBadNumber = "hours_per_day and utilization must each be a number or a numeric string"

// SetCapacity answers PUT /capacity/{user_id}.
func SetCapacity(ctx *context.APIContext) {
	body, ok := readCapacityBody(ctx)
	if !ok {
		return
	}
	hoursPerDay, ok := parseCapacityNumber(body.HoursPerDay)
	if !ok {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body", errCapacityBadNumber, "Send hours_per_day as a number such as 8 or 6.5.")
		return
	}
	utilization, ok := parseCapacityNumber(body.Utilization)
	if !ok {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body", errCapacityBadNumber, "Send utilization as a number such as 0.8.")
		return
	}
	row, err := planning_service.SetCapacity(ctx, ctx.Doer, ctx.PathParamInt64("user_id"),
		planning_service.Scope{RepoID: body.RepoID, OrgID: body.OrgID}, hoursPerDay, utilization, body.Workdays)
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
		return
	}
	ctx.JSON(http.StatusOK, row)
}

// ClearCapacity answers DELETE /capacity/{user_id}.
func ClearCapacity(ctx *context.APIContext) {
	body, ok := readCapacityBody(ctx)
	if !ok {
		return
	}
	row, err := planning_service.ClearCapacity(ctx, ctx.Doer, ctx.PathParamInt64("user_id"),
		planning_service.Scope{RepoID: body.RepoID, OrgID: body.OrgID})
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
		return
	}
	ctx.JSON(http.StatusOK, row)
}

// capacityRoadmapRepo resolves and authorizes the repository the chart covers, 404 for one not
// even visible to the caller AND for one visible but whose Issues unit the caller cannot read —
// the same rule GET /fields applies, so a private repository is never distinguished from one
// that does not exist.
func capacityRoadmapRepo(ctx *context.APIContext, repoID int64) (*repo_model.Repository, bool) {
	if repoID <= 0 {
		hubapi.APIError(ctx, http.StatusBadRequest, "missing_repo_id",
			"repo_id is required: the roadmap's capacity covers one repository's issues",
			"Pass ?repo_id=<id>, listing "+BasePath+"/repos to find it.")
		return nil, false
	}
	return issueTypeReadableRepo(ctx, repoID)
}

// truncateIssues trims issues to at most maxIssues entries, reporting whether more than that
// many were given — a pure function so the boundary (2000 kept, 2001 trimmed and flagged) is
// unit-testable against a fake list rather than a database seeded with 2001 real issues.
func truncateIssues(issues issues_model.IssueList, maxIssues int) (issues_model.IssueList, bool) {
	if len(issues) > maxIssues {
		return issues[:maxIssues], true
	}
	return issues, false
}

// capacityIssues reads a repository's open, assigned issues, up to the page cap, and reports
// whether that cap was hit. It asks for one more than the cap so a full page and a truncated
// one are told apart without a second round trip.
func capacityIssues(ctx *context.APIContext, repo *repo_model.Repository) (issues_model.IssueList, bool, error) {
	opts := &issues_model.IssuesOptions{
		RepoIDs: []int64{repo.ID}, IsPull: optional.Some(false), IsClosed: optional.Some(false),
		AssigneeID: "(any)",
		Paginator:  &db.ListOptions{Page: 1, PageSize: maxCapacityIssues + 1},
		SortType:   "oldest",
	}
	issues, err := issues_model.Issues(ctx, opts)
	if err != nil {
		return nil, false, err
	}
	issues, truncated := truncateIssues(issues, maxCapacityIssues)
	return issues, truncated, nil
}

// parseCapacityDate reads one YYYY-MM-DD query value, empty meaning "not given".
func parseCapacityDate(ctx *context.APIContext, raw, field string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_date",
			field+" is not a YYYY-MM-DD date: "+raw, "Send "+field+" as YYYY-MM-DD, such as 2026-03-01.")
		return time.Time{}, false
	}
	return t.UTC(), true
}

// defaultCapacityWindow is the roadmap's own window: the earliest bar start to the latest bar
// end. A computed default that would exceed the endpoint's own cap is clamped to its most
// recent span rather than refused — bad_window is for a window the CALLER asked for.
func defaultCapacityWindow(bars []planning_service.Bar) (time.Time, time.Time) {
	if len(bars) == 0 {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		return today, today
	}
	var minStart, maxEnd int64
	for i, b := range bars {
		if i == 0 || b.StartUnix < minStart {
			minStart = b.StartUnix
		}
		if i == 0 || b.EndUnix > maxEnd {
			maxEnd = b.EndUnix
		}
	}
	from := time.Unix(minStart, 0).UTC().Truncate(24 * time.Hour)
	to := time.Unix(maxEnd, 0).UTC().Truncate(24 * time.Hour)
	if windowDays(from, to) > maxCapacityWindowDays {
		from = to.AddDate(0, 0, -(maxCapacityWindowDays - 1))
	}
	return from, to
}

func windowDays(from, to time.Time) int {
	return int(to.Sub(from).Hours()/24) + 1
}

// capacityWindow resolves and validates the [from, to] window: a caller-supplied date wins,
// the roadmap's own window fills in whichever side is omitted, and the whole span is checked
// against the endpoint's own cap regardless of where each side came from.
func capacityWindow(ctx *context.APIContext, fromRaw, toRaw string, bars []planning_service.Bar) (time.Time, time.Time, bool) {
	from, ok := parseCapacityDate(ctx, fromRaw, "from")
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	to, ok := parseCapacityDate(ctx, toRaw, "to")
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	if fromRaw == "" || toRaw == "" {
		defaultFrom, defaultTo := defaultCapacityWindow(bars)
		if fromRaw == "" {
			from = defaultFrom
		}
		if toRaw == "" {
			to = defaultTo
		}
	}
	if to.Before(from) {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "bad_window",
			"to is before from", "Swap from and to, or widen the window.")
		return time.Time{}, time.Time{}, false
	}
	if days := windowDays(from, to); days > maxCapacityWindowDays {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "bad_window",
			fmt.Sprintf("the window is %d days, more than the %d this endpoint reads", days, maxCapacityWindowDays),
			fmt.Sprintf("Narrow from and to to at most %d days apart.", maxCapacityWindowDays))
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

// capacityLoadItems reduces issues to the load items SpreadLoad and SprintLoad share: one per
// (issue, assignee), remaining work divided evenly among the issue's own assignees, over the
// window ResolveBar drew for it. An issue with no bar — no type, no parent and no start — has
// no window to spread load over and is skipped, the same "not managed" line GetRoadmap draws.
func capacityLoadItems(ctx *context.APIContext, repo *repo_model.Repository, issues issues_model.IssueList) ([]planning_service.LoadItem, error) {
	parents, err := planning_service.ParentMap(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	hasChildren := make(map[int64]bool, len(parents))
	for _, parentID := range parents {
		hasChildren[parentID] = true
	}
	hier := hierarchyMaps{parents: parents, depths: planning_service.Depths(parents), hasChildren: hasChildren}

	ids := issueIDsOf(issues)
	starts, err := planning_service.IssueStarts(ctx, ids)
	if err != nil {
		return nil, err
	}
	assigned, err := planning_service.Assignments(ctx, ids)
	if err != nil {
		return nil, err
	}
	values, err := planning_service.ValuesFor(ctx, repo, ids)
	if err != nil {
		return nil, err
	}
	tracked, err := planning_model.TrackedByIssueUser(ctx, ids)
	if err != nil {
		return nil, err
	}

	items := make([]planning_service.LoadItem, 0, len(issues))
	for _, issue := range issues {
		bar, ok := planning_service.ResolveBar(barInputFor(issue, starts[issue.ID], assigned[issue.ID], hier, values[issue.ID]))
		if !ok || len(issue.Assignees) == 0 {
			continue
		}
		n := int64(len(issue.Assignees))
		points := float64(planning_service.PointsOf(values[issue.ID])) / float64(n)
		for _, assignee := range issue.Assignees {
			remaining := max(issue.TimeEstimate-tracked[[2]int64{issue.ID, assignee.ID}], 0)
			items = append(items, planning_service.LoadItem{
				IssueID: issue.ID, Number: issue.Index, Title: issue.Title, UserID: assignee.ID,
				MilestoneID:      issue.MilestoneID,
				RemainingSeconds: remaining / n, Points: points,
				StartUnix: bar.StartUnix, EndUnix: bar.EndUnix,
			})
		}
	}
	return items, nil
}

// capacitySprints splits a repository's milestones into the ones SprintLoad can fold — a
// recorded start and a due date both present — and the ones missing one or the other.
func capacitySprints(ctx *context.APIContext, repo *repo_model.Repository) ([]planning_service.Sprint, []RoadmapCapacitySprintUnscheduled, error) {
	milestones, err := db.Find[issues_model.Milestone](ctx, issues_model.FindMilestoneOptions{RepoID: repo.ID})
	if err != nil {
		return nil, nil, err
	}
	milestoneIDs := make([]int64, 0, len(milestones))
	for _, m := range milestones {
		milestoneIDs = append(milestoneIDs, m.ID)
	}
	starts, err := planning_service.MilestoneStarts(ctx, milestoneIDs)
	if err != nil {
		return nil, nil, err
	}

	sprints := make([]planning_service.Sprint, 0, len(milestones))
	unscheduled := make([]RoadmapCapacitySprintUnscheduled, 0, len(milestones))
	for _, m := range milestones {
		start := starts[m.ID]
		due := int64(m.DeadlineUnix)
		var missing []string
		if start == 0 {
			missing = append(missing, "start")
		}
		if due == 0 {
			missing = append(missing, "due")
		}
		if len(missing) > 0 {
			unscheduled = append(unscheduled, RoadmapCapacitySprintUnscheduled{MilestoneID: m.ID, Title: m.Name, Missing: missing})
			continue
		}
		sprints = append(sprints, planning_service.Sprint{MilestoneID: m.ID, Title: m.Name, StartUnix: start, EndUnix: due})
	}
	return sprints, unscheduled, nil
}

// GetRoadmapCapacity answers GET /roadmap/capacity.
func GetRoadmapCapacity(ctx *context.APIContext) {
	q, ok := hubapi.ParseQuery(ctx, roadmapCapacitySpec)
	if !ok {
		return
	}
	repo, ok := capacityRoadmapRepo(ctx, hubapi.EqualityFilterInt(q, "repo_id"))
	if !ok {
		return
	}

	issues, truncated, err := capacityIssues(ctx, repo)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if err := issues.LoadAttributes(ctx); err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	items, err := capacityLoadItems(ctx, repo, issues)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	bars := make([]planning_service.Bar, 0, len(items))
	for _, item := range items {
		bars = append(bars, planning_service.Bar{StartUnix: item.StartUnix, EndUnix: item.EndUnix})
	}
	from, to, ok := capacityWindow(ctx, hubapi.EqualityFilterString(q, "from"), hubapi.EqualityFilterString(q, "to"), bars)
	if !ok {
		return
	}

	assignees, err := repo_model.GetRepoAssignees(ctx, repo)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	usersByID := make(map[int64]*user_model.User, len(assignees))
	for _, u := range assignees {
		usersByID[u.ID] = u
	}
	caps, err := planning_service.ResolveCapacity(ctx, repo, userIDsOf(assignees))
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	sprints, sprintsUnscheduled, err := capacitySprints(ctx, repo)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	out := &RoadmapCapacity{
		Lanes: []RoadmapCapacityLane{}, Sprints: []RoadmapCapacitySprint{},
		SprintsUnscheduled: sprintsUnscheduled, Truncated: truncated,
	}
	for _, lane := range planning_service.SpreadLoad(items, caps, from, to) {
		u := usersByID[lane.UserID]
		if u == nil {
			continue
		}
		c := caps[lane.UserID]
		out.Lanes = append(out.Lanes, RoadmapCapacityLane{
			UserID: u.ID, Login: u.Name, DisplayName: u.DisplayName(), AvatarURL: u.AvatarLink(ctx),
			HoursPerDay: c.HoursPerDay, Utilization: c.Utilization, Workdays: c.Workdays,
			TotalLoadHours: lane.TotalLoadHours, TotalAvailableHours: lane.TotalAvailableHours, Over: lane.Over,
			Days: lane.Days, Unestimated: lane.Unestimated,
		})
	}
	for _, row := range planning_service.SprintLoad(items, caps, sprints) {
		lanes := make([]RoadmapCapacitySprintLane, 0, len(row.Lanes))
		for _, l := range row.Lanes {
			u := usersByID[l.UserID]
			if u == nil {
				continue
			}
			lanes = append(lanes, RoadmapCapacitySprintLane{
				UserID: l.UserID, Login: u.Name, LoadHours: l.LoadHours, AvailableHours: l.AvailableHours,
				Over: l.Over, Points: l.Points,
			})
		}
		out.Sprints = append(out.Sprints, RoadmapCapacitySprint{
			MilestoneID: row.MilestoneID, Title: row.Title, StartUnix: row.StartUnix, EndUnix: row.EndUnix,
			WorkingDays: row.WorkingDays, Lanes: lanes,
		})
	}
	ctx.JSON(http.StatusOK, out)
}
