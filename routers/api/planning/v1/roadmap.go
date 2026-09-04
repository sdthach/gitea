// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	"gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/optional"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	"gitea.dev/services/hub/query"
	planning_service "gitea.dev/services/planning"
)

// roadmapSpec is the roadmap projection's whitelist declaration. Like the matrix and the
// board it is a projection rather than a table, so its parameters select what to project;
// they still go through the one grammar, so an unknown one is refused.
var roadmapSpec = query.Spec{
	Resource: "roadmap",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "parent_issue_id", Column: "parent_issue_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "milestone_id", Column: "milestone_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "state", Column: "state", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
		{Name: "group_by", Column: "group_by", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
		{Name: "zoom", Column: "zoom", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
	},
	PrimaryKey: "issue_id",
	Paging:     query.PagingOffset,
}

// roadmapView is the pair of view settings the chart is read through. Neither is stored, so
// two people may read the same plan at different depths.
type roadmapView struct {
	grouping planning_service.Grouping
	zoom     planning_service.Zoom
}

// RoadmapRuler is the chart's time axis: the unit follows the span being drawn, and every
// tick sits on a unit boundary in UTC. The write granularity stays a day at every unit.
type RoadmapRuler struct {
	Unit      string                  `json:"unit"`
	StartUnix int64                   `json:"start_unix"`
	EndUnix   int64                   `json:"end_unix"`
	Ticks     []planning_service.Tick `json:"ticks"`
}

// Roadmap is the roadmap resource's response shape.
type Roadmap struct {
	RepoID       int64                        `json:"repo_id"`
	RepoFullName string                       `json:"repo_full_name"`
	Bars         []planning_service.Bar       `json:"bars"`
	Arrows       []planning_service.Arrow     `json:"arrows"`
	Rollups      []planning_service.RollupRow `json:"rollups"`
	Unmanaged    []planning_service.Unmanaged `json:"unmanaged"`
	// GroupBy and Zoom echo the view the chart was read at, so a client rendering the
	// response does not have to remember what it asked for.
	GroupBy string `json:"group_by"`
	Zoom    string `json:"zoom"`
	// Groups group the bars by the board's own group definition, empty when grouping is off.
	Groups []planning_service.Group `json:"groups"`
	Ruler  RoadmapRuler             `json:"ruler"`
	// Milestones are the repository's milestones, which are the rows an issue can be filed
	// under. A milestone holding no issue has no rollup, so the chart could not otherwise
	// name it as a destination.
	Milestones []RoadmapMilestone `json:"milestones"`
	// Tree is every recorded parent edge among this repository's issues, so a client can
	// draw the hierarchy without re-deriving it from each bar's own parent_issue_id.
	Tree []planning_service.TreeEdge `json:"tree"`
	// Types are the types visible from this repository, nearest scope shadowing by name —
	// what a bar's type picker offers.
	Types []planning_service.VisibleType `json:"types"`
	// Fields are the custom fields visible from this repository. Bars carry the values
	// themselves.
	Fields []planning_service.VisibleField `json:"fields"`
	// Labels are the repository's own labels plus its owning organization's.
	Labels []LabelRef `json:"labels"`
	// CanWrite says whether the caller may write on the Issues unit, so a client offers the
	// chart's edits only to someone the endpoints will accept them from.
	CanWrite bool `json:"can_write"`
	// Truncated says the issue set hit the page limit, so the chart is a prefix rather
	// than the whole repository. A silently capped chart would be a wrong picture that
	// does not say so.
	Truncated bool `json:"truncated"`
}

// RoadmapMilestone is one milestone the chart draws as a row.
type RoadmapMilestone struct {
	MilestoneID int64  `json:"milestone_id"`
	Title       string `json:"title"`
	IsClosed    bool   `json:"is_closed"`
	StartUnix   int64  `json:"start_unix"`
	EndUnix     int64  `json:"end_unix"`
}

func getRoadmapEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getRoadmap", Method: http.MethodGet, Path: "/roadmap",
			Summary: "The roadmap: one bar per issue, with dependency arrows",
			Description: "Needs no Projects API, so it renders on a build the board cannot. " +
				"Gitea stores no start date — Issue.DeadlineUnix is a single endpoint — so a bar's start comes from the " +
				"recorded schedule this API's own PUT /issues/{issue_id}/schedule writes, falling back to the issue's " +
				"creation time. Its end is the close time when closed, " +
				"the deadline when set, and otherwise the effort estimate applied to the start. " +
				"EVERY bar names the source of its start and of its end, and an inferred end is flagged, because presenting " +
				"an estimate as a measurement is this view's characteristic failure. " +
				"Arrows distinguish depends_on, which Gitea's issue_dependency enforces, from predecessor, which is a " +
				"sequencing hint enforced by nothing. " +
				"An issue with no type, no parent and no start date has no start to draw: it is listed with that reason " +
				"rather than given a fabricated bar. Parent and milestone rows span earliest start to latest end of " +
				"their children, and their progress is the same task-close percentage every other view uses; no second " +
				"definition is introduced. Those rows are computed from their own fetch of every child, not from the " +
				"bars that got drawn, so a parent whose declared window ends before the work filed under it is still " +
				"flagged at zoom=parent where no child is drawn; a rollup whose fetch hit its cap is marked partial and " +
				"publishes no progress percentage. " +
				"zoom selects the depth the chart is read at and group_by the group dimension, reusing the board's own " +
				"groups. A rolled-up zoom pages over its own rows rather than over issues — parent over every issue that " +
				"is itself a recorded parent, milestone over the repository's milestones — so a page of N holds N rollups " +
				"and truncated means more of THOSE than the page holds. parent_issue_id narrows the chart to one issue's " +
				"subtree. " +
				"ruler carries the time axis, whose unit follows the span: day, week, month or quarter. " +
				"tree carries every recorded parent edge. " +
				"Scoped by Gitea's own permission check on the Issues unit. " +
				"The project page's roadmap view is a client of this endpoint.",
			Tag: "roadmap", Query: &roadmapSpec, Response: "Roadmap", ResponseIs: "object",
		},
		Handler: GetRoadmap,
	}
}

// GetRoadmap answers GET /roadmap.
func GetRoadmap(ctx *context.APIContext) {
	q, ok := hubapi.ParseQuery(ctx, roadmapSpec)
	if !ok {
		return
	}
	repo, perm, ok := roadmapRepo(ctx, hubapi.EqualityFilterInt(q, "repo_id"))
	if !ok {
		return
	}

	state, ok := parseRoadmapState(ctx, hubapi.EqualityFilterString(q, "state"))
	if !ok {
		return
	}
	grouping, ok := parseGrouping(ctx, hubapi.EqualityFilterString(q, "group_by"))
	if !ok {
		return
	}
	zoom, ok := parseZoom(ctx, hubapi.EqualityFilterString(q, "zoom"))
	if !ok {
		return
	}

	opts := &issues_model.IssuesOptions{
		RepoIDs:   []int64{repo.ID},
		IsPull:    optional.Some(false),
		IsClosed:  state,
		Paginator: &db.ListOptions{Page: q.Page, PageSize: q.Limit},
		SortType:  "oldest",
	}
	parents, err := planning_service.ParentMap(ctx, repo.ID)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	parentFilter := hubapi.EqualityFilterInt(q, "parent_issue_id")
	switch {
	case zoom == planning_service.ZoomParent:
		// Every issue that is itself a recorded parent seeds its own rollup: a page of
		// parents is a page of rollups rather than a prefix of the issues filed under them,
		// and truncated then means more PARENTS than the page holds. parent_issue_id still
		// narrows this to one parent's own subtree of parents.
		ids := distinctParentIDs(parents)
		if parentFilter > 0 {
			allowed := map[int64]bool{parentFilter: true}
			for _, id := range planning_service.Subtree(parents, parentFilter) {
				allowed[id] = true
			}
			narrowed := ids[:0]
			for _, id := range ids {
				if allowed[id] {
					narrowed = append(narrowed, id)
				}
			}
			ids = narrowed
		}
		if len(ids) == 0 {
			ids = []int64{0}
		}
		opts.IssueIDs = ids
	case parentFilter > 0:
		ids := planning_service.Subtree(parents, parentFilter)
		if len(ids) == 0 {
			// An id no issue ever has: IssuesOptions reads this as a real filter matching
			// nothing, never as no filter at all.
			ids = []int64{0}
		}
		opts.IssueIDs = ids
	}
	if milestoneID := hubapi.EqualityFilterInt(q, "milestone_id"); milestoneID > 0 {
		opts.MilestoneIDs = []int64{milestoneID}
	}

	renderRoadmap(ctx, repo, opts, q.Limit, perm.CanWrite(unit.TypeIssues),
		roadmapView{grouping: grouping, zoom: zoom})
}

// distinctParentIDs lists every id appearing as a value in parents — every issue that is
// itself a recorded parent, regardless of depth. []int64{0} stands for "nothing is a parent
// here", which IssuesOptions reads as a real filter rather than as no filter at all.
func distinctParentIDs(parents map[int64]int64) []int64 {
	seen := make(map[int64]bool, len(parents))
	ids := make([]int64, 0, len(parents))
	for _, parentID := range parents {
		if !seen[parentID] {
			seen[parentID] = true
			ids = append(ids, parentID)
		}
	}
	if len(ids) == 0 {
		return []int64{0}
	}
	return ids
}

// parseZoom refuses an unknown depth naming what is accepted, exactly as the board refuses an
// unknown grouping, rather than drawing a chart the caller did not ask for.
func parseZoom(ctx *context.APIContext, raw string) (planning_service.Zoom, bool) {
	zoom, ok := planning_service.ParseZoom(raw)
	if !ok {
		ctx.JSON(http.StatusBadRequest, &query.Error{
			Status: http.StatusBadRequest, Code: "unknown_zoom",
			Message:         "no such chart zoom: " + raw,
			Parameter:       "zoom",
			Accepted:        planning_service.Zooms,
			SuggestedAction: "Read the chart at one of " + strings.Join(planning_service.Zooms, ", ") + ", or omit zoom for one bar per issue.",
		})
		return planning_service.ZoomIssue, false
	}
	return zoom, true
}

// renderRoadmap projects one repository's issues and answers with the chart. Every write
// endpoint replies through it, so a caller never has to re-fetch to see what its write did,
// and the chart it gets back is the one GET would have produced.
func renderRoadmap(ctx *context.APIContext, repo *repo_model.Repository, opts *issues_model.IssuesOptions, limit int, canWrite bool, view roadmapView) {
	// A write endpoint replying through here states no view, and the defaults are the ones
	// a caller that passes no parameter gets.
	if view.grouping == "" {
		view.grouping = planning_service.GroupNone
	}
	if view.zoom == "" {
		view.zoom = planning_service.ZoomIssue
	}

	out := &Roadmap{
		RepoID: repo.ID, RepoFullName: repo.FullName(),
		Bars: []planning_service.Bar{}, Arrows: []planning_service.Arrow{},
		Rollups: []planning_service.RollupRow{}, Unmanaged: []planning_service.Unmanaged{},
		Groups: []planning_service.Group{}, Milestones: []RoadmapMilestone{},
		GroupBy: string(view.grouping), Zoom: string(view.zoom),
		Types: []planning_service.VisibleType{}, Labels: []LabelRef{}, CanWrite: canWrite,
	}

	types, err := planning_service.TypesFor(ctx, repo)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	out.Types = types

	fields, err := planning_service.FieldsFor(ctx, repo)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	out.Fields = fields

	labels, err := repoLabels(ctx, repo)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	out.Labels = labels

	parents, err := planning_service.ParentMap(ctx, repo.ID)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	out.Tree = planning_service.BuildTree(parents)
	hasChildren := make(map[int64]bool, len(parents))
	for _, parentID := range parents {
		hasChildren[parentID] = true
	}
	hier := hierarchyMaps{parents: parents, depths: planning_service.Depths(parents), hasChildren: hasChildren}

	milestones, err := db.Find[issues_model.Milestone](ctx, issues_model.FindMilestoneOptions{RepoID: repo.ID})
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	milestoneIDs := make([]int64, 0, len(milestones))
	for _, m := range milestones {
		milestoneIDs = append(milestoneIDs, m.ID)
	}
	milestoneStarts, err := planning_service.MilestoneStarts(ctx, milestoneIDs)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	for _, m := range milestones {
		out.Milestones = append(out.Milestones, RoadmapMilestone{
			MilestoneID: m.ID, Title: m.Name, IsClosed: m.IsClosed,
			StartUnix: milestoneStarts[m.ID], EndUnix: int64(m.DeadlineUnix),
		})
	}

	if view.zoom == planning_service.ZoomMilestone {
		children := newRolledChildren("milestone")
		if out.Rollups, out.Truncated, err = roadmapMilestoneRollups(ctx, repo, opts, limit, children, hier); err != nil {
			ctx.APIErrorInternal(err)
			return
		}
		if out.Arrows, err = roadmapRolledArrows(ctx, children); err != nil {
			ctx.APIErrorInternal(err)
			return
		}
		out.Ruler = rulerOver(out.Bars, out.Rollups)
		ctx.JSON(http.StatusOK, out)
		return
	}

	issues, err := issues_model.Issues(ctx, opts)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if err := issues.LoadAttributes(ctx); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	out.Truncated = len(issues) == limit

	starts, err := planning_service.IssueStarts(ctx, issueIDsOf(issues))
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	assigned, err := planning_service.Assignments(ctx, issueIDsOf(issues))
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	values, err := planning_service.ValuesFor(ctx, repo, issueIDsOf(issues))
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	bars := make([]planning_service.Bar, 0, len(issues))
	drawn := make(map[int64]bool, len(issues))
	for _, issue := range issues {
		in := barInputFor(ctx, issue, starts[issue.ID], assigned[issue.ID], hier, values[issue.ID])
		if bar, ok := planning_service.ResolveBar(in); ok {
			bars = append(bars, bar)
			drawn[issue.ID] = true
			continue
		}
		// Listed beside the chart with the reason, never given a fabricated bar.
		out.Unmanaged = append(out.Unmanaged, planning_service.UnmanagedFor(in))
	}

	// At zoom=parent the fetch already narrowed to exactly the structural parent set
	// (distinctParentIDs), so that IS the seed rather than something to re-derive from the
	// fetched issues' OWN parent link — which, for a root parent, is zero.
	var seedParentIDs []int64
	if view.zoom == planning_service.ZoomParent {
		seedParentIDs = issueIDsOf(issues)
	}
	children := newRolledChildren("parent")
	rollups, err := roadmapRollups(ctx, repo, bars, opts.IsClosed, children, hier, seedParentIDs)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	// A rolled-up row is a bracket over a set its children define, so parent zoom lists the
	// brackets alone: drawing the children beside them is the issue zoom. Its arrows are the
	// children's edges re-keyed onto the brackets, so an edge does not vanish with the bar.
	if view.zoom == planning_service.ZoomParent {
		out.Rollups = rollupsOfKind(rollups, "parent")
		out.Arrows, err = roadmapRolledArrows(ctx, children)
	} else {
		out.Bars, out.Rollups = bars, rollups
		out.Arrows, err = roadmapArrows(ctx, issues, drawn)
	}
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	// Groups group the bars the response publishes, so a zoom that publishes none carries none.
	if view.grouping != planning_service.GroupNone && len(out.Bars) > 0 {
		out.Groups = planning_service.RoadmapGroups(out.Bars, view.grouping)
		// A parent-grouped row's root may be unmanaged or off this chart entirely, exactly
		// as on the board: its title is fetched here rather than left blank.
		if missing := planning_service.GroupsMissingRootTitle(out.Groups); len(missing) > 0 {
			roots, err := issues_model.GetIssuesByIDs(ctx, missing)
			if err != nil {
				ctx.APIErrorInternal(err)
				return
			}
			titles := make(map[int64]string, len(roots))
			for _, root := range roots {
				titles[root.ID] = root.Title
			}
			planning_service.ApplyRootTitles(out.Groups, titles)
		}
	}
	out.Ruler = rulerOver(out.Bars, out.Rollups)
	ctx.JSON(http.StatusOK, out)
}

func rollupsOfKind(rollups []planning_service.RollupRow, kind string) []planning_service.RollupRow {
	out := make([]planning_service.RollupRow, 0, len(rollups))
	for _, rollup := range rollups {
		if rollup.Kind == kind {
			out = append(out, rollup)
		}
	}
	return out
}

// rulerOver lays the axis over what this zoom draws, so the unit follows the range on screen.
func rulerOver(bars []planning_service.Bar, rollups []planning_service.RollupRow) RoadmapRuler {
	ruler := RoadmapRuler{Ticks: []planning_service.Tick{}}
	seen := false
	cover := func(startUnix, endUnix int64) {
		if !seen || startUnix < ruler.StartUnix {
			ruler.StartUnix = startUnix
		}
		if !seen || endUnix > ruler.EndUnix {
			ruler.EndUnix = endUnix
		}
		seen = true
	}
	for _, bar := range bars {
		cover(bar.StartUnix, bar.EndUnix)
	}
	for _, rollup := range rollups {
		cover(rollup.StartUnix, rollup.EndUnix)
	}
	if !seen {
		// An axis invented over an empty chart would date a picture of nothing.
		ruler.Unit = planning_service.RulerDay
		return ruler
	}
	ruler.Unit, ruler.Ticks = planning_service.RulerFor(ruler.StartUnix, ruler.EndUnix)
	return ruler
}

// roadmapRollups folds each parent from its own fetch: over the drawn bars the containment
// check goes vacuous at zoom=parent, where no child is drawn at all.
//
// seedParentIDs, when non-nil, IS the candidate set — used at zoom=parent, where the fetch
// already narrowed to exactly the structural parent set and a root parent's own bar carries
// ParentIssueID 0. nil falls back to discovering candidates from the drawn bars' own
// ParentIssueID, which is what a plain issue-zoom fetch needs.
func roadmapRollups(ctx *context.APIContext, repo *repo_model.Repository, bars []planning_service.Bar, state optional.Option[bool], children *rolledChildren, hier hierarchyMaps, seedParentIDs []int64) ([]planning_service.RollupRow, error) {
	var parentIDs []int64
	if seedParentIDs != nil {
		parentIDs = append([]int64(nil), seedParentIDs...)
	} else {
		seenParent := map[int64]bool{}
		for _, bar := range bars {
			if bar.ParentIssueID != 0 && !seenParent[bar.ParentIssueID] {
				seenParent[bar.ParentIssueID] = true
				parentIDs = append(parentIDs, bar.ParentIssueID)
			}
		}
	}
	milestones := make([]int64, 0, 8)
	seenMilestone := map[int64]bool{}
	for _, bar := range bars {
		if bar.MilestoneID > 0 && !seenMilestone[bar.MilestoneID] {
			seenMilestone[bar.MilestoneID] = true
			milestones = append(milestones, bar.MilestoneID)
		}
	}
	slices.Sort(parentIDs)
	slices.Sort(milestones)

	rows := make([]planning_service.RollupRow, 0, len(parentIDs)+len(milestones))
	for _, parentID := range parentIDs {
		row, held, ok, err := roadmapParentRollup(ctx, repo, state, parentID, hier)
		if err != nil {
			return nil, err
		}
		if ok {
			rows = append(rows, row)
			children.collect(row, held)
		}
	}
	for _, milestoneID := range milestones {
		row, held, ok, err := roadmapRollup(ctx, repo, state, "milestone", strconv.FormatInt(milestoneID, 10),
			func(o *issues_model.IssuesOptions) { o.MilestoneIDs = []int64{milestoneID} }, hier)
		if err != nil {
			return nil, err
		}
		if ok {
			rows = append(rows, row)
			children.collect(row, held)
		}
	}
	return rows, nil
}

// directChildIDs lists every issue whose recorded parent is exactly parentID, read from the
// repository's own full parent map rather than a query — the map already holds every edge —
// and sorted so a capped fetch keeps the same prefix on every call.
func directChildIDs(parents map[int64]int64, parentID int64) []int64 {
	ids := make([]int64, 0, 4)
	for child, p := range parents {
		if p == parentID {
			ids = append(ids, child)
		}
	}
	slices.Sort(ids)
	return ids
}

// roadmapParentRollup folds one parent's DIRECT children into its row. The repository's whole
// parent map is already in memory, so every child is found there rather than queried — but a
// parent with more children than query.MaxLimit is still capped and marked partial, the same
// safety a milestone rollup's own paginated fetch gets.
func roadmapParentRollup(ctx *context.APIContext, repo *repo_model.Repository, state optional.Option[bool], parentID int64, hier hierarchyMaps) (planning_service.RollupRow, issues_model.IssueList, bool, error) {
	childIDs := directChildIDs(hier.parents, parentID)
	capped := len(childIDs) > query.MaxLimit
	if capped {
		childIDs = childIDs[:query.MaxLimit]
	}
	ids := make([]int64, 0, len(childIDs)+1)
	ids = append(ids, childIDs...)
	ids = append(ids, parentID)
	issues, err := issues_model.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return planning_service.RollupRow{}, nil, false, err
	}
	if err := issues.LoadAttributes(ctx); err != nil {
		return planning_service.RollupRow{}, nil, false, err
	}
	var parentIssue *issues_model.Issue
	for _, iss := range issues {
		if iss.ID == parentID {
			parentIssue = iss
			break
		}
	}
	starts, err := planning_service.IssueStarts(ctx, issueIDsOf(issues))
	if err != nil {
		return planning_service.RollupRow{}, nil, false, err
	}
	assigned, err := planning_service.Assignments(ctx, issueIDsOf(issues))
	if err != nil {
		return planning_service.RollupRow{}, nil, false, err
	}
	values, err := planning_service.ValuesFor(ctx, repo, issueIDsOf(issues))
	if err != nil {
		return planning_service.RollupRow{}, nil, false, err
	}
	bars := make([]planning_service.Bar, 0, len(issues))
	held := make(issues_model.IssueList, 0, len(issues))
	for _, issue := range issues {
		if state.Has() && issue.IsClosed != state.Value() {
			continue
		}
		held = append(held, issue)
		if bar, ok := planning_service.ResolveBar(barInputFor(ctx, issue, starts[issue.ID], assigned[issue.ID], hier, values[issue.ID])); ok {
			bars = append(bars, bar)
		}
	}
	key := strconv.FormatInt(parentID, 10)
	for _, row := range planning_service.BuildRollups(bars) {
		if row.Kind == "parent" && row.Key == key {
			// The parent's own title, from the fetch above — not from the bar the fold built,
			// which is empty whenever the parent itself is unmanaged or filtered out by state.
			if parentIssue != nil {
				row.Label = parentIssue.Title
			}
			if capped {
				row.MarkPartial()
			}
			return row, held, true, nil
		}
	}
	return planning_service.RollupRow{}, nil, false, nil
}

// roadmapMilestoneRollups pages over the repository's milestones rather than its issues, so a
// page of N holds N milestone rows and truncated means more milestones than the page holds.
func roadmapMilestoneRollups(ctx *context.APIContext, repo *repo_model.Repository, opts *issues_model.IssuesOptions, limit int, children *rolledChildren, hier hierarchyMaps) ([]planning_service.RollupRow, bool, error) {
	// The milestone's OWN open/closed state is not the state filter: state narrows the
	// ISSUES, here as everywhere else, so an open milestone holding closed work is listed
	// at state=closed and a milestone whose children all fall outside it yields no row.
	find := issues_model.FindMilestoneOptions{
		RepoID: repo.ID, SortType: "id",
		ListOptions: db.ListOptions{Page: opts.Paginator.Page, PageSize: limit},
	}
	milestones, err := db.Find[issues_model.Milestone](ctx, find)
	if err != nil {
		return nil, false, err
	}
	rows := make([]planning_service.RollupRow, 0, len(milestones))
	for _, milestone := range milestones {
		// The milestone_id filter narrows the chart here, because this zoom's page is over
		// milestones rather than over the issues it would otherwise have narrowed.
		if len(opts.MilestoneIDs) > 0 && !slices.Contains(opts.MilestoneIDs, milestone.ID) {
			continue
		}
		row, held, ok, err := roadmapRollup(ctx, repo, opts.IsClosed, "milestone", strconv.FormatInt(milestone.ID, 10),
			func(o *issues_model.IssuesOptions) { o.MilestoneIDs = []int64{milestone.ID} }, hier)
		if err != nil {
			return nil, false, err
		}
		if ok {
			rows = append(rows, row)
			children.collect(row, held)
		}
	}
	return rows, len(opts.MilestoneIDs) == 0 && len(milestones) == limit, nil
}

// roadmapRollup folds one parent's children into its row, one query per parent because a
// child's window cannot be resolved in SQL. ok=false for a parent whose children draw no bar.
func roadmapRollup(ctx *context.APIContext, repo *repo_model.Repository, state optional.Option[bool], kind, key string, narrow func(*issues_model.IssuesOptions), hier hierarchyMaps) (planning_service.RollupRow, issues_model.IssueList, bool, error) {
	opts := &issues_model.IssuesOptions{
		RepoIDs:   []int64{repo.ID},
		IsPull:    optional.Some(false),
		IsClosed:  state,
		Paginator: &db.ListOptions{Page: 1, PageSize: query.MaxLimit},
		SortType:  "oldest",
	}
	narrow(opts)

	issues, err := issues_model.Issues(ctx, opts)
	if err != nil {
		return planning_service.RollupRow{}, nil, false, err
	}
	if err := issues.LoadAttributes(ctx); err != nil {
		return planning_service.RollupRow{}, nil, false, err
	}
	starts, err := planning_service.IssueStarts(ctx, issueIDsOf(issues))
	if err != nil {
		return planning_service.RollupRow{}, nil, false, err
	}
	assigned, err := planning_service.Assignments(ctx, issueIDsOf(issues))
	if err != nil {
		return planning_service.RollupRow{}, nil, false, err
	}
	values, err := planning_service.ValuesFor(ctx, repo, issueIDsOf(issues))
	if err != nil {
		return planning_service.RollupRow{}, nil, false, err
	}
	children := make([]planning_service.Bar, 0, len(issues))
	for _, issue := range issues {
		if bar, ok := planning_service.ResolveBar(barInputFor(ctx, issue, starts[issue.ID], assigned[issue.ID], hier, values[issue.ID])); ok {
			children = append(children, bar)
		}
	}

	for _, row := range planning_service.BuildRollups(children) {
		if row.Kind != kind || row.Key != key {
			continue
		}
		if len(issues) >= query.MaxLimit {
			row.MarkPartial()
		}
		return row, issues, true, nil
	}
	return planning_service.RollupRow{}, nil, false, nil
}

// roadmapRepo resolves and authorizes the repository the chart covers.
func roadmapRepo(ctx *context.APIContext, repoID int64) (*repo_model.Repository, access.Permission, bool) {
	var perm access.Permission
	if repoID <= 0 {
		hubapi.APIError(ctx, http.StatusBadRequest, "missing_repo_id",
			"repo_id is required: a roadmap covers one repository's issues",
			"Pass ?repo_id=<id>, listing "+BasePath+"/repos to find it.")
		return nil, perm, false
	}
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository with that id is visible to you",
			"Check the id against "+BasePath+"/repos.")
		return nil, perm, false
	}
	perm, err = access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, perm, false
	}
	if !perm.CanRead(unit.TypeIssues) {
		hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
			"your account cannot read the Issues unit of "+repo.FullName(),
			"Ask a repository administrator for read access.")
		return nil, perm, false
	}
	return repo, perm, true
}

// roadmapStates is the accepted set for ?state=.
var roadmapStates = []string{"open", "closed", "all"}

func parseRoadmapState(ctx *context.APIContext, raw string) (optional.Option[bool], bool) {
	switch raw {
	case "", "all":
		return optional.None[bool](), true
	case "open":
		return optional.Some(false), true
	case "closed":
		return optional.Some(true), true
	}
	ctx.JSON(http.StatusBadRequest, &query.Error{
		Status: http.StatusBadRequest, Code: "unknown_state",
		Message:         "no such issue state: " + raw,
		Parameter:       "state",
		Accepted:        roadmapStates,
		SuggestedAction: "Use state=open, state=closed or state=all; all is the default, because a chart that hid closed work would show no finished bar.",
	})
	return optional.None[bool](), false
}

// hierarchyMaps is one repository's whole plan_issue_parent table, read once per request and
// threaded through every bar and rollup computation rather than re-queried per issue.
type hierarchyMaps struct {
	parents     map[int64]int64
	depths      map[int64]int
	hasChildren map[int64]bool
}

// barInputFor reduces one issue to what bar resolution depends on. assigned is the issue's own
// type assignment, zero when it has none; values is its own custom field values, nil when it
// has none.
func barInputFor(ctx *context.APIContext, issue *issues_model.Issue, startedUnix int64, assigned planning_service.AssignedType, hier hierarchyMaps, values map[string]any) planning_service.BarInput {
	in := planning_service.BarInput{
		IssueID: issue.ID, Number: issue.Index, Title: issue.Title, URL: issue.Link(),
		ScheduledStartUnix: startedUnix,
		TypeID:             assigned.TypeID, TypeName: assigned.Name, TypeColor: assigned.Color, TypeIcon: assigned.Icon,
		CreatedUnix:   int64(issue.CreatedUnix),
		ClosedUnix:    int64(issue.ClosedUnix),
		DeadlineUnix:  int64(issue.DeadlineUnix),
		EffortSeconds: planning_service.ParseEffortSeconds(issue.Content),
		IsClosed:      issue.IsClosed,
		Labels:        make([]string, 0, len(issue.Labels)),
		Assignees:     make([]string, 0, len(issue.Assignees)),
		ParentIssueID: hier.parents[issue.ID],
		RootIssueID:   planning_service.RootOf(hier.parents, issue.ID),
		Depth:         hier.depths[issue.ID],
		HasChildren:   hier.hasChildren[issue.ID],
		Fields:        valuesOrEmpty(values),
		// Loaded in one batched query per projection by issues.LoadAttributes (TotalTrackedTime)
		// and as a plain issue column (TimeEstimate) — no per-issue facets call needed.
		TimeEstimate:   issue.TimeEstimate,
		TrackedSeconds: issue.TotalTrackedTime,
	}
	for _, label := range issue.Labels {
		in.Labels = append(in.Labels, label.Name)
	}
	in.AssigneeAvatars = make([]planning_service.AssigneeAvatar, 0, len(issue.Assignees))
	for _, assignee := range issue.Assignees {
		in.Assignees = append(in.Assignees, assignee.Name)
		in.AssigneeAvatars = append(in.AssigneeAvatars, planning_service.AssigneeAvatar{Login: assignee.Name, AvatarURL: assignee.AvatarLink(ctx)})
	}
	if issue.Milestone != nil {
		in.MilestoneID, in.Milestone = issue.Milestone.ID, issue.Milestone.Name
	}
	return in
}

// issueIDsOf collects an issue list's ids, for the batch lookups the chart makes over them.
func issueIDsOf(issues issues_model.IssueList) []int64 {
	ids := make([]int64, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}

// arrowEdge is one dependency edge between two issues, before it is attached to whatever the
// chart is drawing them as.
type arrowEdge struct {
	from, to int64
	kind     planning_service.ArrowKind
}

// readArrowEdges reads an issue set's edges from the two sources they live in:
// issue_dependency for the enforced ones, and the rendered cross-reference lines in the body
// for the sequencing ones. It reads a SET of issues, not the drawn ones, so a rolled-up zoom
// can read the edges among children none of which is drawn.
func readArrowEdges(ctx *context.APIContext, issues issues_model.IssueList) ([]arrowEdge, error) {
	edges := make([]arrowEdge, 0, 8)
	ids := make([]int64, 0, len(issues))
	byNumber := make(map[int64]int64, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
		byNumber[issue.Index] = issue.ID
	}
	if len(ids) == 0 {
		return edges, nil
	}

	deps := make([]*issues_model.IssueDependency, 0, len(ids))
	if err := db.GetEngine(ctx).In("issue_id", ids).Find(&deps); err != nil {
		return nil, err
	}
	// A row in issue_dependency IS the depends_on relation, so its arrow kind comes from the
	// same mapping the body-derived words go through.
	gate, ok := planning_service.ArrowKindFor("depends_on")
	if !ok {
		return nil, errors.New("depends_on is not a relation the roadmap can draw")
	}
	for _, dep := range deps {
		// DependencyID blocks IssueID, so the blocker comes first on a schedule.
		edges = append(edges, arrowEdge{dep.DependencyID, dep.IssueID, gate})
	}

	for _, issue := range issues {
		for _, rel := range planning_service.ParseSequenceRelations(issue.Content) {
			number, err := strconv.ParseInt(rel[1], 10, 64)
			if err != nil {
				continue
			}
			// The word decides the arrow: a vocabulary word that carries no ordering draws
			// nothing at all.
			kind, ok := planning_service.ArrowKindFor(rel[0])
			if !ok {
				continue
			}
			other := byNumber[number]
			if rel[0] == "predecessor" {
				edges = append(edges, arrowEdge{other, issue.ID, kind})
			} else {
				edges = append(edges, arrowEdge{issue.ID, other, kind})
			}
		}
	}
	return edges, nil
}

// roadmapArrows attaches the page's edges to the bars it drew. An edge whose other end is
// not drawn is dropped: an arrow to a bar that is not on the chart would point at nothing.
func roadmapArrows(ctx *context.APIContext, issues issues_model.IssueList, drawn map[int64]bool) ([]planning_service.Arrow, error) {
	edges, err := readArrowEdges(ctx, issues)
	if err != nil {
		return nil, err
	}
	arrows := make([]planning_service.Arrow, 0, len(edges))
	seen := map[string]bool{}
	for _, edge := range edges {
		if edge.from == 0 || edge.to == 0 || edge.from == edge.to || !drawn[edge.from] || !drawn[edge.to] {
			continue
		}
		key := strconv.FormatInt(edge.from, 10) + ">" + strconv.FormatInt(edge.to, 10) + ":" + string(edge.kind)
		if seen[key] {
			continue
		}
		seen[key] = true
		arrows = append(arrows, planning_service.NewArrow(edge.from, edge.to, edge.kind))
	}
	return arrows, nil
}

// rolledChildren records which bracket each of a page's rollup children falls in, so an edge
// between two children can be re-keyed onto the brackets that hold them.
type rolledChildren struct {
	kind      string // only rows of this kind are on the page, so only they collect
	rollupKey map[int64]string
	issues    issues_model.IssueList
}

func newRolledChildren(kind string) *rolledChildren {
	return &rolledChildren{kind: kind, rollupKey: map[int64]string{}}
}

func (c *rolledChildren) collect(row planning_service.RollupRow, issues issues_model.IssueList) {
	if c == nil || row.Kind != c.kind {
		return
	}
	for _, issue := range issues {
		if _, seen := c.rollupKey[issue.ID]; seen {
			continue
		}
		c.rollupKey[issue.ID] = row.RollupKey()
		c.issues = append(c.issues, issue)
	}
}

// roadmapRolledArrows re-keys the edges among a page's rollup children onto the brackets
// that hold them, so an edge whose end is inside a bracket attaches to the bracket instead of
// vanishing with the bar it pointed at. The gate/sequence distinction survives the re-keying.
func roadmapRolledArrows(ctx *context.APIContext, children *rolledChildren) ([]planning_service.Arrow, error) {
	edges, err := readArrowEdges(ctx, children.issues)
	if err != nil {
		return nil, err
	}
	arrows := make([]planning_service.Arrow, 0, len(edges))
	seen := map[string]bool{}
	for _, edge := range edges {
		from, to := children.rollupKey[edge.from], children.rollupKey[edge.to]
		// An edge inside one bracket says nothing about the order of the brackets, and an
		// end with no bracket on this page has nothing to attach to.
		if from == "" || to == "" || from == to {
			continue
		}
		key := from + ">" + to + ":" + string(edge.kind)
		if seen[key] {
			continue
		}
		seen[key] = true
		arrow := planning_service.NewArrow(edge.from, edge.to, edge.kind)
		arrow.FromRollup, arrow.ToRollup = from, to
		arrows = append(arrows, arrow)
	}
	return arrows, nil
}
