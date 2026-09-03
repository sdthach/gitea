// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"time"

	actions_model "gitea.dev/models/actions"
	deployments_model "gitea.dev/models/deployments"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/services/hub/query"
)

// The cross-repository CI overview.
//
// Gitea shows runs one repository at a time; this is the aggregate. Its aggregates are
// computed here, in process, over rows reduced by models/deployments — never in dialect-specific
// SQL, because bucketing by UTC day is spelled strftime on SQLite and date_trunc on
// PostgreSQL and one schema has to answer both.
//
// The projections below take plain slices and return plain structs, so every number the page
// shows is proven by a test that needs no database.

// RunState is a run's state as the overview groups them. The five states the tiles
// name are success, failure, in_progress, queued and cancelled; skipped and unknown exist so
// no run is silently dropped from a total.
type RunState string

const (
	StateSuccess    RunState = "success"
	StateFailure    RunState = "failure"
	StateInProgress RunState = "in_progress"
	StateQueued     RunState = "queued"
	StateCancelled  RunState = "cancelled"
	StateSkipped    RunState = "skipped"
	StateUnknown    RunState = "unknown"
)

// RunStates is the complete state set, in the order the tiles render it.
var RunStates = []string{
	string(StateSuccess), string(StateFailure), string(StateInProgress),
	string(StateQueued), string(StateCancelled), string(StateSkipped), string(StateUnknown),
}

// RunStateOf maps Gitea's own run status onto the overview's state. It reads the upstream
// enum rather than restating its integers, so a status added upstream is a compile-time
// concern rather than a silently miscounted tile.
func RunStateOf(status int) RunState {
	switch actions_model.Status(status) {
	case actions_model.StatusSuccess:
		return StateSuccess
	case actions_model.StatusFailure:
		return StateFailure
	case actions_model.StatusRunning, actions_model.StatusCancelling:
		return StateInProgress
	case actions_model.StatusWaiting, actions_model.StatusBlocked:
		return StateQueued
	case actions_model.StatusCancelled:
		return StateCancelled
	case actions_model.StatusSkipped:
		return StateSkipped
	}
	return StateUnknown
}

// RunStateNames is what the /runs resource accepts in a status filter, so a caller filters
// on the name the overview shows rather than on Gitea's internal integer.
func RunStateNames() []string { return append([]string(nil), RunStates...) }

// RunStatusCodes returns the Actions status integers one state name covers. A state can
// cover more than one status, so the filter renders as an IN rather than an equality.
func RunStatusCodes(state string) ([]int64, bool) {
	var codes []int64
	for _, s := range []actions_model.Status{
		actions_model.StatusUnknown, actions_model.StatusSuccess, actions_model.StatusFailure,
		actions_model.StatusCancelled, actions_model.StatusSkipped, actions_model.StatusWaiting,
		actions_model.StatusRunning, actions_model.StatusBlocked, actions_model.StatusCancelling,
	} {
		if string(RunStateOf(int(s))) == state {
			codes = append(codes, int64(s))
		}
	}
	return codes, len(codes) > 0
}

// DefaultWindowDays is the window the overview opens on.
const DefaultWindowDays = 7

// MaxWindowDays caps the selectable window. A wider one would read more runs than
// MaxOverviewRunFacts allows and return a truncated aggregate.
const MaxWindowDays = 365

// Window is the selectable period the tiles summarise. It is half-open — [From, To) —
// so a run on the boundary is counted once, by exactly one of a window and its predecessor.
type Window struct {
	FromUnix int64 `json:"from_unix"`
	ToUnix   int64 `json:"to_unix"`
	Days     int   `json:"days"`
}

// NewWindow builds the window of the given length ending at now. days outside
// [1, MaxWindowDays] is clamped rather than refused: the grammar has already rejected a
// non-integer, and a caller asking for 10000 days wants "as much as you have".
func NewWindow(days int, now time.Time) Window {
	if days <= 0 {
		days = DefaultWindowDays
	}
	if days > MaxWindowDays {
		days = MaxWindowDays
	}
	// The window is half-open, so the upper bound sits one second past now: a run created
	// this very second belongs to the current window, not to the next one that never comes.
	to := now.UTC().Unix() + 1
	return Window{FromUnix: to - int64(days)*86400, ToUnix: to, Days: days}
}

// Previous is the window of equal length immediately before this one, which is what each
// tile is compared against.
func (w Window) Previous() Window {
	span := w.ToUnix - w.FromUnix
	return Window{FromUnix: w.FromUnix - span, ToUnix: w.FromUnix, Days: w.Days}
}

// Summary is one window's tiles.
type Summary struct {
	Window    Window           `json:"window"`
	TotalRuns int64            `json:"total_runs"`
	Runs      map[string]int64 `json:"runs"`
	// SuccessRate is successes over runs that reached a result — success or failure.
	// Dividing by every run instead would make a queue of pending runs look like a
	// regression in quality, which is a different question from the one the tile asks.
	SuccessRate          float64 `json:"success_rate"`
	TotalDurationSeconds int64   `json:"total_duration_seconds"`
	ActiveRepositories   int64   `json:"active_repositories"`
	InactiveRepositories int64   `json:"inactive_repositories"`
	ActiveWorkflows      int64   `json:"active_workflows"`
	DisabledWorkflows    int64   `json:"disabled_workflows"`
}

// SummarizeRuns reduces one window's runs to its tiles. accessibleRepos is the whole set the
// viewer can see, which is what makes "inactive" answerable: a repository with no run in the
// window is inactive, and one the viewer cannot see is neither.
func SummarizeRuns(facts []deployments_model.RunFact, window Window, accessibleRepos int, disabled map[int64][]string) Summary {
	s := Summary{Window: window, Runs: map[string]int64{}}
	for _, name := range RunStates {
		s.Runs[name] = 0
	}

	activeRepos := map[int64]bool{}
	activeWorkflows := map[string]bool{}
	for _, f := range facts {
		s.TotalRuns++
		s.Runs[string(RunStateOf(f.Status))]++
		s.TotalDurationSeconds += f.DurationSeconds()
		activeRepos[f.RepoID] = true
		activeWorkflows[workflowKey(f.RepoID, f.WorkflowID)] = true
	}

	completed := s.Runs[string(StateSuccess)] + s.Runs[string(StateFailure)]
	if completed > 0 {
		s.SuccessRate = float64(s.Runs[string(StateSuccess)]) / float64(completed)
	}

	s.ActiveRepositories = int64(len(activeRepos))
	// A run can outlive the repository's place in the accessible set, so the difference can
	// go negative; the tile reads zero rather than a negative count of repositories.
	s.InactiveRepositories = max(int64(accessibleRepos)-s.ActiveRepositories, 0)
	s.ActiveWorkflows = int64(len(activeWorkflows))
	for _, files := range disabled {
		s.DisabledWorkflows += int64(len(files))
	}
	return s
}

func workflowKey(repoID int64, workflowID string) string {
	return strconv.FormatInt(repoID, 10) + "\x00" + workflowID
}

// RepoStat is one repository's run volume, success rate and average duration.
type RepoStat struct {
	RepoID                 int64   `json:"repo_id"`
	RepoFullName           string  `json:"repo_full_name"`
	Runs                   int64   `json:"runs"`
	Successes              int64   `json:"successes"`
	Failures               int64   `json:"failures"`
	SuccessRate            float64 `json:"success_rate"`
	AverageDurationSeconds int64   `json:"average_duration_seconds"`
}

// WorkflowStat is one workflow's, with whether the repository has it disabled.
type WorkflowStat struct {
	RepoID                 int64   `json:"repo_id"`
	RepoFullName           string  `json:"repo_full_name"`
	WorkflowID             string  `json:"workflow_id"`
	Runs                   int64   `json:"runs"`
	Successes              int64   `json:"successes"`
	Failures               int64   `json:"failures"`
	SuccessRate            float64 `json:"success_rate"`
	AverageDurationSeconds int64   `json:"average_duration_seconds"`
	Disabled               bool    `json:"disabled"`
}

// counter accumulates one group's runs.
type counter struct {
	runs      int64
	successes int64
	failures  int64
	duration  int64
	durations int64
}

func (c *counter) add(f deployments_model.RunFact) {
	c.runs++
	switch RunStateOf(f.Status) {
	case StateSuccess:
		c.successes++
	case StateFailure:
		c.failures++
	}
	if d := f.DurationSeconds(); d > 0 {
		c.duration += d
		c.durations++
	}
}

func (c *counter) rate() float64 {
	completed := c.successes + c.failures
	if completed == 0 {
		return 0
	}
	return float64(c.successes) / float64(completed)
}

func (c *counter) average() int64 {
	if c.durations == 0 {
		return 0
	}
	return c.duration / c.durations
}

// AggregateRepos reduces the window's runs to one row per repository. names supplies the
// owner/name each row links out to; a repository whose name is missing still appears, since
// dropping it would make the totals disagree with the summary.
func AggregateRepos(facts []deployments_model.RunFact, names map[int64]string) []RepoStat {
	groups := map[int64]*counter{}
	for _, f := range facts {
		c := groups[f.RepoID]
		if c == nil {
			c = &counter{}
			groups[f.RepoID] = c
		}
		c.add(f)
	}
	out := make([]RepoStat, 0, len(groups))
	for repoID, c := range groups {
		out = append(out, RepoStat{
			RepoID:                 repoID,
			RepoFullName:           names[repoID],
			Runs:                   c.runs,
			Successes:              c.successes,
			Failures:               c.failures,
			SuccessRate:            c.rate(),
			AverageDurationSeconds: c.average(),
		})
	}
	SortRepoStats(out, "runs", "desc")
	return out
}

// AggregateWorkflows reduces the window's runs to one row per (repository, workflow file).
// A workflow the repository has disabled but which ran inside the window still appears, with
// Disabled set: hiding it would lose the run from the total.
func AggregateWorkflows(facts []deployments_model.RunFact, names map[int64]string, disabled map[int64][]string) []WorkflowStat {
	type wfKey struct {
		repoID     int64
		workflowID string
	}
	groups := map[wfKey]*counter{}
	for _, f := range facts {
		k := wfKey{repoID: f.RepoID, workflowID: f.WorkflowID}
		c := groups[k]
		if c == nil {
			c = &counter{}
			groups[k] = c
		}
		c.add(f)
	}
	out := make([]WorkflowStat, 0, len(groups))
	for k, c := range groups {
		out = append(out, WorkflowStat{
			RepoID:                 k.repoID,
			RepoFullName:           names[k.repoID],
			WorkflowID:             k.workflowID,
			Runs:                   c.runs,
			Successes:              c.successes,
			Failures:               c.failures,
			SuccessRate:            c.rate(),
			AverageDurationSeconds: c.average(),
			Disabled:               slices.Contains(disabled[k.repoID], k.workflowID),
		})
	}
	SortWorkflowStats(out, "runs", "desc")
	return out
}

// SortRepoStats orders rows in process. The rows are a projection rather than a table, so
// sorting cannot be pushed into SQL; the whitelist of sortable fields is still the
// resource's own, checked by the one grammar before it reaches here.
//
// Every order is tie-broken on the repository id, so paging a projection repeats and skips
// no row.
func SortRepoStats(rows []RepoStat, column, order string) {
	desc := order != query.OrderAsc
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		var an, bn float64
		var as, bs string
		switch column {
		case "repo_id":
			an, bn = float64(a.RepoID), float64(b.RepoID)
		case "repo_full_name":
			as, bs = a.RepoFullName, b.RepoFullName
		case "success_rate":
			an, bn = a.SuccessRate, b.SuccessRate
		case "average_duration_seconds":
			an, bn = float64(a.AverageDurationSeconds), float64(b.AverageDurationSeconds)
		default: // runs
			an, bn = float64(a.Runs), float64(b.Runs)
		}
		if as != bs {
			return desc != (as < bs)
		}
		if an != bn {
			return desc != (an < bn)
		}
		return a.RepoID < b.RepoID
	})
}

// SortWorkflowStats orders workflow rows in process, tie-broken on (repo_id, workflow_id).
func SortWorkflowStats(rows []WorkflowStat, column, order string) {
	desc := order != query.OrderAsc
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		var an, bn float64
		var as, bs string
		switch column {
		case "repo_id":
			an, bn = float64(a.RepoID), float64(b.RepoID)
		case "workflow_id":
			as, bs = a.WorkflowID, b.WorkflowID
		case "success_rate":
			an, bn = a.SuccessRate, b.SuccessRate
		case "average_duration_seconds":
			an, bn = float64(a.AverageDurationSeconds), float64(b.AverageDurationSeconds)
		default: // runs
			an, bn = float64(a.Runs), float64(b.Runs)
		}
		if as != bs {
			return desc != (as < bs)
		}
		if an != bn {
			return desc != (an < bn)
		}
		if a.RepoID != b.RepoID {
			return a.RepoID < b.RepoID
		}
		return a.WorkflowID < b.WorkflowID
	})
}

// TrendPoint is one UTC day of the daily series.
type TrendPoint struct {
	Date                   string `json:"date"`
	DayUnix                int64  `json:"day_unix"`
	Runs                   int64  `json:"runs"`
	Successes              int64  `json:"successes"`
	Failures               int64  `json:"failures"`
	AverageDurationSeconds int64  `json:"average_duration_seconds"`
	Deployments            int64  `json:"deployments"`
}

// DailyTrend buckets the window into UTC days, one point per day including days with no run
// at all — a gap in the series would be read as missing data rather than as a quiet day.
//
// Deployments come from the fork's own delivery_deployment rather than from counting deploy
// runs, so this dashboard and the delivery grid cannot disagree about how many deploys
// happened.
func DailyTrend(facts []deployments_model.RunFact, deployments []deployments_model.DeploymentFact, window Window) []TrendPoint {
	start := dayStart(window.FromUnix)
	end := dayStart(window.ToUnix)

	buckets := map[int64]*counter{}
	deploys := map[int64]int64{}
	for _, f := range facts {
		day := dayStart(f.CreatedUnix)
		c := buckets[day]
		if c == nil {
			c = &counter{}
			buckets[day] = c
		}
		c.add(f)
	}
	for _, d := range deployments {
		deploys[dayStart(d.CreatedUnix)]++
	}

	out := make([]TrendPoint, 0, window.Days+1)
	for day := start; day <= end; day += 86400 {
		c := buckets[day]
		if c == nil {
			c = &counter{}
		}
		out = append(out, TrendPoint{
			Date:                   time.Unix(day, 0).UTC().Format(time.DateOnly),
			DayUnix:                day,
			Runs:                   c.runs,
			Successes:              c.successes,
			Failures:               c.failures,
			AverageDurationSeconds: c.average(),
			Deployments:            deploys[day],
		})
	}
	return out
}

// dayStart truncates a unix second to the start of its UTC day. Doing it in Go rather than in
// SQL is what keeps one schema answering both SQLite and PostgreSQL.
func dayStart(unix int64) int64 {
	return unix - modFloor(unix, 86400)
}

// modFloor is a floored modulo, so a timestamp before the epoch buckets into the day it
// belongs to rather than the one after it.
func modFloor(a, m int64) int64 {
	r := a % m
	if r < 0 {
		r += m
	}
	return r
}

// OverviewOptions narrows an aggregate. RepoIDs is always the caller's own accessible set,
// resolved by Gitea's existing permission filtering before it reaches here: a run
// in a repository the viewer cannot read must not appear in any figure.
type OverviewOptions struct {
	RepoIDs []int64
	RepoID  int64 // 0 means every repository in RepoIDs
	Window  Window
}

// scope resolves the repository set the aggregate reads. It is fail-CLOSED: an empty
// accessible set aggregates nothing, and a RepoID outside the accessible set narrows to
// nothing rather than widening to everything.
func (o OverviewOptions) scope() []int64 {
	if len(o.RepoIDs) == 0 {
		return nil
	}
	if o.RepoID == 0 {
		return o.RepoIDs
	}
	if !containsID(o.RepoIDs, o.RepoID) {
		return nil
	}
	return []int64{o.RepoID}
}

// Overview is the composite: the window's summary beside the previous window of equal
// length. It exists to save round trips, never as the only way to reach the data — every
// number in it is independently queryable from /runs, /workflows and /overview/repos.
type Overview struct {
	Summary  Summary `json:"summary"`
	Previous Summary `json:"previous"`
	// Truncated says the window held more runs than one aggregate reads, so the numbers
	// are a floor rather than a total. A silently capped aggregate would be a wrong number
	// that does not say so.
	Truncated bool `json:"truncated"`
}

// BuildOverview assembles the composite. It is the endpoint's whole implementation: the
// /overview handler resolves permissions and the window, and calls this.
func BuildOverview(ctx context.Context, opts OverviewOptions) (*Overview, error) {
	repoIDs := opts.scope()
	if len(repoIDs) == 0 {
		empty := SummarizeRuns(nil, opts.Window, 0, nil)
		return &Overview{Summary: empty, Previous: SummarizeRuns(nil, opts.Window.Previous(), 0, nil)}, nil
	}

	disabled, err := deployments_model.FindDisabledWorkflows(ctx, repoIDs)
	if err != nil {
		return nil, err
	}
	current, truncatedNow, err := deployments_model.FindRunFacts(ctx, repoIDs, opts.Window.FromUnix, opts.Window.ToUnix)
	if err != nil {
		return nil, err
	}
	previousWindow := opts.Window.Previous()
	previous, truncatedBefore, err := deployments_model.FindRunFacts(ctx, repoIDs, previousWindow.FromUnix, previousWindow.ToUnix)
	if err != nil {
		return nil, err
	}
	return &Overview{
		Summary:   SummarizeRuns(current, opts.Window, len(repoIDs), disabled),
		Previous:  SummarizeRuns(previous, previousWindow, len(repoIDs), disabled),
		Truncated: truncatedNow || truncatedBefore,
	}, nil
}

// BuildTrends assembles the daily series.
func BuildTrends(ctx context.Context, opts OverviewOptions) ([]TrendPoint, bool, error) {
	repoIDs := opts.scope()
	if len(repoIDs) == 0 {
		return DailyTrend(nil, nil, opts.Window), false, nil
	}
	facts, truncated, err := deployments_model.FindRunFacts(ctx, repoIDs, opts.Window.FromUnix, opts.Window.ToUnix)
	if err != nil {
		return nil, false, err
	}
	deployments, err := deployments_model.FindDeploymentFacts(ctx, repoIDs, opts.Window.FromUnix, opts.Window.ToUnix)
	if err != nil {
		return nil, false, err
	}
	return DailyTrend(facts, deployments, opts.Window), truncated, nil
}

// BuildRepoStats assembles one row per repository.
func BuildRepoStats(ctx context.Context, opts OverviewOptions) ([]RepoStat, bool, error) {
	repoIDs := opts.scope()
	if len(repoIDs) == 0 {
		return []RepoStat{}, false, nil
	}
	facts, truncated, err := deployments_model.FindRunFacts(ctx, repoIDs, opts.Window.FromUnix, opts.Window.ToUnix)
	if err != nil {
		return nil, false, err
	}
	names, err := repoNames(ctx, repoIDs)
	if err != nil {
		return nil, false, err
	}
	return AggregateRepos(facts, names), truncated, nil
}

// BuildWorkflowStats assembles one row per (repository, workflow file).
func BuildWorkflowStats(ctx context.Context, opts OverviewOptions) ([]WorkflowStat, bool, error) {
	repoIDs := opts.scope()
	if len(repoIDs) == 0 {
		return []WorkflowStat{}, false, nil
	}
	facts, truncated, err := deployments_model.FindRunFacts(ctx, repoIDs, opts.Window.FromUnix, opts.Window.ToUnix)
	if err != nil {
		return nil, false, err
	}
	names, err := repoNames(ctx, repoIDs)
	if err != nil {
		return nil, false, err
	}
	disabled, err := deployments_model.FindDisabledWorkflows(ctx, repoIDs)
	if err != nil {
		return nil, false, err
	}
	return AggregateWorkflows(facts, names, disabled), truncated, nil
}

// repoNames resolves owner/name for the rows to link out to. Every per-repository detail
// links to Gitea's own page; the overview duplicates none of them.
func repoNames(ctx context.Context, repoIDs []int64) (map[int64]string, error) {
	repos, err := repo_model.GetRepositoriesMapByIDs(ctx, repoIDs)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(repos))
	for id, r := range repos {
		if r != nil {
			names[id] = r.FullName()
		}
	}
	return names, nil
}
