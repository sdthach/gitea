// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"gitea.dev/models/db"
	delivery_model "gitea.dev/models/delivery"
	repo_model "gitea.dev/models/repo"

	"xorm.io/builder"
)

// CellState is one release × environment cell of the grid (E7).
//
// Every state is a PROJECTION over the append-only audit log; none of them is stored, and
// no row carries an updatable status column the grid reads (E3, E5). The `✔ ×N` rendering
// the requirement lists is the repeat count decorating a deployed cell rather than a state
// of its own — a cell can be both current and deployed twice, which SC 14 asks for
// explicitly, so the count cannot be mutually exclusive with `✔ now`.
type CellState string

const (
	CellNever      CellState = "never"       // ·
	CellLive       CellState = "live"        // ✔ now
	CellSuperseded CellState = "superseded"  // ✔
	CellFailed     CellState = "failed"      // ✗
	CellInProgress CellState = "in_progress" // ⟳
	CellHeld       CellState = "held"        // ⏸
)

// Cell is one grid cell.
type Cell struct {
	Environment string    `json:"environment"`
	State       CellState `json:"state"`
	// Symbol is the rendering E7 tabulates. It is computed here rather than in the
	// template so the CLI, the page and the tests all read the same string.
	Symbol string `json:"symbol"`
	// Successes counts how many times this release reached this environment. `✔ ×N`.
	Successes    int    `json:"successes"`
	RunID        int64  `json:"run_id"`
	RunURL       string `json:"run_url"`
	OccurredUnix int64  `json:"occurred_unix"`
}

// GridRow is one release, with one cell per environment in configured order (E7).
type GridRow struct {
	RepoID       int64  `json:"repo_id"`
	RepoFullName string `json:"repo_full_name"`
	ReleaseTag   string `json:"release_tag"`
	ReleaseURL   string `json:"release_url"`
	CreatedUnix  int64  `json:"created_unix"`
	Cells        []Cell `json:"cells"`
}

// Event is the projection's input: one audit row reduced to what a cell state depends on.
// Taking a reduced struct rather than the model keeps the projection pure and testable with
// no database (J5, J10).
type Event struct {
	ID           int64
	ReleaseTag   string
	Environment  string
	Event        string
	OccurredUnix int64
	RunID        int64
	RunURL       string
}

// cellSymbol renders a cell. It is the single spelling of the seven renderings E7 lists.
func cellSymbol(state CellState, successes int) string {
	switch state {
	case CellNever:
		return "·"
	case CellFailed:
		return "✗"
	case CellInProgress:
		return "⟳"
	case CellHeld:
		return "⏸"
	}
	symbol := "✔"
	if successes > 1 {
		symbol += fmt.Sprintf(" ×%d", successes)
	}
	if state == CellLive {
		symbol += " now"
	}
	return symbol
}

// aggregate is what one (release, environment) group reduces to.
type aggregate struct {
	successes    int
	last         Event
	lastSuccess  Event
	hasEvents    bool
	hasSucceeded bool
}

// key identifies one cell.
type key struct {
	release     string
	environment string
}

// ProjectCells projects the append-only log onto one cell per (release, environment).
//
// policies maps an environment name to its approval policy. `⏸` renders from the
// environment record, never inferred from run status, which cannot distinguish a held
// deploy from a queued one (E15). Slice 6 gives the state a second source; the projection
// already answers it here so the grid does not change shape when it arrives.
//
// The result is keyed by release tag, and each slice holds one cell per environment in the
// order environments were given — sequence is configuration, since nothing in Gitea
// expresses it (E7).
func ProjectCells(environments, releases []string, events []Event, policies map[string]string) map[string][]Cell {
	ordered := append([]Event(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].OccurredUnix != ordered[j].OccurredUnix {
			return ordered[i].OccurredUnix < ordered[j].OccurredUnix
		}
		return ordered[i].ID < ordered[j].ID
	})

	groups := map[key]*aggregate{}
	for _, e := range ordered {
		k := key{release: e.ReleaseTag, environment: e.Environment}
		agg := groups[k]
		if agg == nil {
			agg = &aggregate{}
			groups[k] = agg
		}
		agg.hasEvents = true
		agg.last = e
		if e.Event == delivery_model.AuditSucceeded {
			agg.successes++
			agg.hasSucceeded = true
			agg.lastSuccess = e
		}
	}

	// The release currently live in an environment is the one whose most recent success
	// there is the latest. It is read from the log, never from a "current" flag a second
	// deploy would have to remember to clear.
	live := map[string]string{}
	latest := map[string]Event{}
	for k, agg := range groups {
		if !agg.hasSucceeded {
			continue
		}
		best, seen := latest[k.environment]
		if !seen || agg.lastSuccess.OccurredUnix > best.OccurredUnix ||
			(agg.lastSuccess.OccurredUnix == best.OccurredUnix && agg.lastSuccess.ID > best.ID) {
			latest[k.environment] = agg.lastSuccess
			live[k.environment] = k.release
		}
	}

	out := make(map[string][]Cell, len(releases))
	for _, release := range releases {
		cells := make([]Cell, 0, len(environments))
		for _, env := range environments {
			agg := groups[key{release: release, environment: env}]
			cells = append(cells, projectOne(env, release, agg, live[env], policies[env]))
		}
		out[release] = cells
	}
	return out
}

// projectOne reduces one group to its cell.
func projectOne(environment, release string, agg *aggregate, liveRelease, policy string) Cell {
	cell := Cell{Environment: environment, State: CellNever}
	if agg == nil || !agg.hasEvents {
		cell.Symbol = cellSymbol(cell.State, 0)
		return cell
	}

	cell.Successes = agg.successes
	cell.RunID = agg.last.RunID
	cell.RunURL = agg.last.RunURL
	cell.OccurredUnix = agg.last.OccurredUnix

	switch agg.last.Event {
	case delivery_model.AuditSucceeded:
		cell.State = CellSuperseded
		if liveRelease == release {
			cell.State = CellLive
		}
	case delivery_model.AuditFailed, delivery_model.AuditCancelled, delivery_model.AuditRejected:
		// "last attempt failed" is what the cell reports, whatever came before it.
		cell.State = CellFailed
	case delivery_model.AuditRequested:
		// A requested deploy into a gated environment is held, not queued. The distinction
		// comes from the environment record; run status cannot express it (E15).
		cell.State = CellInProgress
		if policy != "" && policy != delivery_model.PolicyNone {
			cell.State = CellHeld
		}
	default:
		// started, approved: the run is on its way.
		cell.State = CellInProgress
	}
	cell.Symbol = cellSymbol(cell.State, cell.Successes)
	return cell
}

// GridOptions narrows the grid. The grid spans the repositories the viewer can see, so
// RepoIDs is always the caller's own accessible set, resolved by Gitea's existing
// permission filtering before it reaches here (E10, E12).
type GridOptions struct {
	RepoIDs     []int64
	RepoID      int64 // 0 means every repository in RepoIDs
	ReleaseTag  string
	Environment string
	Limit       int
	Offset      int
}

// BuildGrid assembles the release × environment grid.
//
// Releases are read from Gitea's own Release model at render time. Nothing is synced,
// cached or mirrored, so a release cut outside this feature appears immediately (E6).
func BuildGrid(ctx context.Context, opts GridOptions) ([]*GridRow, int64, error) {
	if len(opts.RepoIDs) == 0 {
		return []*GridRow{}, 0, nil
	}
	repoIDs := opts.RepoIDs
	if opts.RepoID != 0 {
		if !containsID(repoIDs, opts.RepoID) {
			return []*GridRow{}, 0, nil
		}
		repoIDs = []int64{opts.RepoID}
	}

	releaseCond := builder.In("repo_id", repoIDs).
		And(builder.Eq{"is_draft": false, "is_tag": false})
	if opts.ReleaseTag != "" {
		releaseCond = releaseCond.And(builder.Eq{"tag_name": opts.ReleaseTag})
	}

	sess := db.GetEngine(ctx).Where(releaseCond).OrderBy("created_unix DESC, id DESC")
	if opts.Limit > 0 {
		sess = sess.Limit(opts.Limit, opts.Offset)
	}
	releases := make([]*repo_model.Release, 0, 16)
	total, err := sess.FindAndCount(&releases)
	if err != nil {
		return nil, 0, err
	}
	if len(releases) == 0 {
		return []*GridRow{}, total, nil
	}

	// The environment column order, the audit log and the repository are read once per
	// repository rather than once per release, so a page of releases costs a fixed number
	// of queries however many rows it holds.
	byRepo := map[int64][]string{}
	for _, r := range releases {
		byRepo[r.RepoID] = append(byRepo[r.RepoID], r.TagName)
	}
	cellsOf := map[int64]map[string][]Cell{}
	repos := map[int64]*repo_model.Repository{}
	for repoID, tags := range byRepo {
		environments, policies, err := environmentsOf(ctx, repoID, opts.Environment)
		if err != nil {
			return nil, 0, err
		}
		events, err := eventsOf(ctx, repoID, tags)
		if err != nil {
			return nil, 0, err
		}
		cellsOf[repoID] = ProjectCellsHeld(ctx, repoID, environments, tags, events, policies)
		repo, err := repo_model.GetRepositoryByID(ctx, repoID)
		if err != nil {
			return nil, 0, err
		}
		repos[repoID] = repo
	}

	rows := make([]*GridRow, 0, len(releases))
	for _, release := range releases {
		release.Repo = repos[release.RepoID]
		rows = append(rows, &GridRow{
			RepoID:       release.RepoID,
			RepoFullName: release.Repo.FullName(),
			ReleaseTag:   release.TagName,
			ReleaseURL:   release.HTMLURL(),
			CreatedUnix:  int64(release.CreatedUnix),
			Cells:        cellsOf[release.RepoID][release.TagName],
		})
	}
	return rows, total, nil
}

// environmentsOf reads a repository's environment column order, falling back to the
// instance-wide default set when the repository has declared none of its own (E7).
func environmentsOf(ctx context.Context, repoID int64, only string) ([]string, map[string]string, error) {
	envs, _, err := delivery_model.FindEnvironments(ctx, builder.Eq{"repo_id": repoID}, "sort_order ASC, id ASC", 0, 0)
	if err != nil {
		return nil, nil, err
	}
	if len(envs) == 0 {
		envs, _, err = delivery_model.FindEnvironments(ctx,
			builder.Eq{"repo_id": delivery_model.DefaultsRepoID}, "sort_order ASC, id ASC", 0, 0)
		if err != nil {
			return nil, nil, err
		}
	}
	names := make([]string, 0, len(envs))
	policies := make(map[string]string, len(envs))
	for _, env := range envs {
		if only != "" && env.Name != delivery_model.NormalizeEnvironmentName(only) {
			continue
		}
		names = append(names, env.Name)
		policies[env.Name] = env.ApprovalPolicy
	}
	return names, policies, nil
}

// eventsOf reads the audit rows the projection needs for one repository's releases.
func eventsOf(ctx context.Context, repoID int64, tags []string) ([]Event, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	cond := builder.Eq{"repo_id": repoID}.And(builder.In("release_tag", tags))
	rows, err := delivery_model.FindAuditEvents(ctx, cond, "occurred_unix ASC, id ASC", 0)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(rows))
	for _, r := range rows {
		events = append(events, Event{
			ID:           r.ID,
			ReleaseTag:   r.ReleaseTag,
			Environment:  r.Environment,
			Event:        r.Event,
			OccurredUnix: int64(r.OccurredUnix),
			RunID:        r.RunID,
			RunURL:       r.RunURL,
		})
	}
	return events, nil
}

func containsID(ids []int64, want int64) bool { return slices.Contains(ids, want) }
