// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

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

	"xorm.io/builder"
)

// timelineSpec is the timeline projection's whitelist declaration. Like the grid and the
// board it is a projection rather than a table, so its parameters select what to project;
// they still go through the one grammar, so an unknown one is refused.
var timelineSpec = query.Spec{
	Resource: "timeline",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "epic", Column: "epic", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
		{Name: "milestone_id", Column: "milestone_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "state", Column: "state", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
		{Name: "group_by", Column: "group_by", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
		{Name: "zoom", Column: "zoom", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
	},
	PrimaryKey: "issue_id",
	Paging:     query.PagingOffset,
}

// timelineView is the pair of view settings the chart is read through. Neither is stored, so
// two people may read the same plan at different depths.
type timelineView struct {
	grouping planning_service.Grouping
	zoom     planning_service.Zoom
	// epic narrows the chart to one epic. At zoom=epic the fetch selects epic issues
	// rather than one epic's issues, so it is applied to the bars instead of in SQL.
	epic string
}

// TimelineRuler is the chart's time axis: the unit follows the span being drawn, and every
// tick sits on a unit boundary in UTC. The write granularity stays a day at every unit.
type TimelineRuler struct {
	Unit      string                  `json:"unit"`
	StartUnix int64                   `json:"start_unix"`
	EndUnix   int64                   `json:"end_unix"`
	Ticks     []planning_service.Tick `json:"ticks"`
}

// Timeline is the timeline resource's response shape.
type Timeline struct {
	RepoID       int64                        `json:"repo_id"`
	RepoFullName string                       `json:"repo_full_name"`
	Bars         []planning_service.Bar       `json:"bars"`
	Arrows       []planning_service.Arrow     `json:"arrows"`
	Spans        []planning_service.SpanRow   `json:"spans"`
	Unmanaged    []planning_service.Unmanaged `json:"unmanaged"`
	// GroupBy and Zoom echo the view the chart was read at, so a client rendering the
	// response does not have to remember what it asked for.
	GroupBy string `json:"group_by"`
	Zoom    string `json:"zoom"`
	// Lanes group the bars by the board's own lane definition, empty when grouping is off.
	Lanes []planning_service.Lane `json:"lanes"`
	Ruler TimelineRuler           `json:"ruler"`
	// Rows are the repository's milestones, which are the rows an issue can be filed under.
	// A milestone holding no issue has no span, so the chart could not otherwise name it as
	// a destination.
	Rows []TimelineRow `json:"rows"`
	// CanWrite says whether the caller may write on the Issues unit, so a client offers the
	// chart's edits only to someone the endpoints will accept them from.
	CanWrite bool `json:"can_write"`
	// Truncated says the issue set hit the page limit, so the chart is a prefix rather
	// than the whole repository. A silently capped chart would be a wrong picture that
	// does not say so.
	Truncated bool `json:"truncated"`
}

// TimelineRow is one milestone the chart draws as a row.
type TimelineRow struct {
	MilestoneID int64  `json:"milestone_id"`
	Title       string `json:"title"`
	IsClosed    bool   `json:"is_closed"`
}

func getTimelineEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getTimeline", Method: http.MethodGet, Path: "/timeline",
			Summary: "The delivery timeline: one bar per issue, with dependency arrows",
			Description: "Needs no Projects API, so it renders on a build the board cannot. " +
				"Gitea stores no start date — Issue.DeadlineUnix is a single endpoint — so a bar's start comes from ccpm: " +
				"the `started:` in updates/<N>/progress.md, carried onto the issue by issue-sync as a `ccpm:started=` marker " +
				"on the progress comment, falling back to the issue's creation time. Its end is the close time when closed, " +
				"the deadline when set, and otherwise the effort estimate applied to the start. " +
				"EVERY bar names the source of its start and of its end, and an inferred end is flagged, because presenting " +
				"an estimate as a measurement is this view's characteristic failure. " +
				"Arrows distinguish depends_on, which Gitea's issue_dependency enforces, from predecessor, which is a " +
				"sequencing hint enforced by nothing. " +
				"An issue ccpm does not manage has no start to draw: it is listed with that reason rather than given a " +
				"fabricated bar. Epic and milestone rows span earliest start to latest end of their children, and " +
				"their progress is ccpm's own task-close percentage; no second definition is introduced. Those rows are " +
				"computed from their own fetch of every child, not from the bars that got drawn, so an epic whose declared " +
				"window ends before the work filed under it is still flagged at zoom=epic where no child is drawn; a rollup " +
				"whose fetch hit its cap is marked partial and publishes no progress percentage. " +
				"zoom selects the depth the chart is read at and group_by the lane dimension, reusing the board's own lanes. " +
				"A rolled-up zoom pages over its own rows rather than over issues — epic over the type:epic issues, milestone " +
				"over the repository's milestones — so a page of N holds N rollups and truncated means more of THOSE than the " +
				"page holds. An epic with no children yet is still listed, over its own declared window. " +
				"ruler carries the time axis, whose unit follows the span: day, week, month or quarter. " +
				"Scoped by Gitea's own permission check on the Issues unit. " +
				"The /delivery/timeline page is a client of this endpoint.",
			Tag: "timeline", Query: &timelineSpec, Response: "Timeline", ResponseIs: "object",
		},
		Handler: GetTimeline,
	}
}

// GetTimeline answers GET /timeline.
func GetTimeline(ctx *context.APIContext) {
	q, ok := hubapi.ParseQuery(ctx, timelineSpec)
	if !ok {
		return
	}
	repo, perm, ok := timelineRepo(ctx, hubapi.EqualityFilterInt(q, "repo_id"))
	if !ok {
		return
	}

	state, ok := parseTimelineState(ctx, hubapi.EqualityFilterString(q, "state"))
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
	epic := hubapi.EqualityFilterString(q, "epic")
	switch {
	case zoom == planning_service.ZoomEpic:
		// An epic issue carries epic:<its own name>, so it seeds its own rollup: a page of
		// epics is a page of rollups rather than a prefix of the issues filed under them,
		// and truncated then means more EPICS than the page holds.
		opts.IncludedLabelNames = []string{planning_service.TypeLabelPrefix + planning_service.TypeEpic}
	case epic != "":
		opts.IncludedLabelNames = []string{planning_service.EpicLabelPrefix + epic}
	}
	if milestoneID := hubapi.EqualityFilterInt(q, "milestone_id"); milestoneID > 0 {
		opts.MilestoneIDs = []int64{milestoneID}
	}

	renderTimeline(ctx, repo, opts, q.Limit, perm.CanWrite(unit.TypeIssues),
		timelineView{grouping: grouping, zoom: zoom, epic: epic})
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

// renderTimeline projects one repository's issues and answers with the chart. Every write
// endpoint replies through it, so a caller never has to re-fetch to see what its write did,
// and the chart it gets back is the one GET would have produced.
func renderTimeline(ctx *context.APIContext, repo *repo_model.Repository, opts *issues_model.IssuesOptions, limit int, canWrite bool, view timelineView) {
	// A write endpoint replying through here states no view, and the defaults are the ones
	// a caller that passes no parameter gets.
	if view.grouping == "" {
		view.grouping = planning_service.GroupNone
	}
	if view.zoom == "" {
		view.zoom = planning_service.ZoomIssue
	}

	out := &Timeline{
		RepoID: repo.ID, RepoFullName: repo.FullName(),
		Bars: []planning_service.Bar{}, Arrows: []planning_service.Arrow{},
		Spans: []planning_service.SpanRow{}, Unmanaged: []planning_service.Unmanaged{},
		Lanes: []planning_service.Lane{}, Rows: []TimelineRow{},
		GroupBy: string(view.grouping), Zoom: string(view.zoom),
		CanWrite: canWrite,
	}

	milestones, err := db.Find[issues_model.Milestone](ctx, issues_model.FindMilestoneOptions{RepoID: repo.ID})
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	for _, m := range milestones {
		out.Rows = append(out.Rows, TimelineRow{MilestoneID: m.ID, Title: m.Name, IsClosed: m.IsClosed})
	}

	if view.zoom == planning_service.ZoomMilestone {
		children := newRolledChildren("milestone")
		if out.Spans, out.Truncated, err = timelineMilestoneRollups(ctx, repo, opts, limit, children); err != nil {
			ctx.APIErrorInternal(err)
			return
		}
		if out.Arrows, err = timelineRolledArrows(ctx, children); err != nil {
			ctx.APIErrorInternal(err)
			return
		}
		out.Ruler = rulerOver(out.Bars, out.Spans)
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

	starts, err := ccpmStarts(ctx, issues)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	bars := make([]planning_service.Bar, 0, len(issues))
	drawn := make(map[int64]bool, len(issues))
	for _, issue := range issues {
		in := barInputFor(issue, starts[issue.ID])
		if bar, ok := planning_service.ResolveBar(in); ok {
			bars = append(bars, bar)
			drawn[issue.ID] = true
			continue
		}
		// Listed beside the chart with the reason, never given a fabricated bar.
		out.Unmanaged = append(out.Unmanaged, planning_service.UnmanagedFor(in))
	}

	if view.zoom == planning_service.ZoomEpic && view.epic != "" {
		bars = slices.DeleteFunc(bars, func(bar planning_service.Bar) bool { return bar.Epic != view.epic })
	}

	children := newRolledChildren("epic")
	spans, err := timelineRollups(ctx, repo, bars, opts.IsClosed, children)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	// A rolled-up row is a bracket over a set its children define, so epic zoom lists the
	// brackets alone: drawing the children beside them is the issue zoom. Its arrows are the
	// children's edges re-keyed onto the brackets, so an edge does not vanish with the bar.
	if view.zoom == planning_service.ZoomEpic {
		out.Spans = spansOfKind(spans, "epic")
		out.Arrows, err = timelineRolledArrows(ctx, children)
	} else {
		out.Bars, out.Spans = bars, spans
		out.Arrows, err = timelineArrows(ctx, issues, drawn)
	}
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	// Lanes group the bars the response publishes, so a zoom that publishes none carries none.
	if view.grouping != planning_service.GroupNone && len(out.Bars) > 0 {
		out.Lanes = planning_service.TimelineLanes(out.Bars, view.grouping)
	}
	out.Ruler = rulerOver(out.Bars, out.Spans)
	ctx.JSON(http.StatusOK, out)
}

func spansOfKind(spans []planning_service.SpanRow, kind string) []planning_service.SpanRow {
	out := make([]planning_service.SpanRow, 0, len(spans))
	for _, span := range spans {
		if span.Kind == kind {
			out = append(out, span)
		}
	}
	return out
}

// rulerOver lays the axis over what this zoom draws, so the unit follows the span on screen.
func rulerOver(bars []planning_service.Bar, spans []planning_service.SpanRow) TimelineRuler {
	ruler := TimelineRuler{Ticks: []planning_service.Tick{}}
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
	for _, span := range spans {
		cover(span.StartUnix, span.EndUnix)
	}
	if !seen {
		// An axis invented over an empty chart would date a picture of nothing.
		ruler.Unit = planning_service.RulerDay
		return ruler
	}
	ruler.Unit, ruler.Ticks = planning_service.RulerFor(ruler.StartUnix, ruler.EndUnix)
	return ruler
}

// timelineRollups folds each parent from its own fetch: over the drawn bars the containment
// check goes vacuous at zoom=epic, where no child is drawn at all.
func timelineRollups(ctx *context.APIContext, repo *repo_model.Repository, bars []planning_service.Bar, state optional.Option[bool], children *rolledChildren) ([]planning_service.SpanRow, error) {
	epics := make([]string, 0, 8)
	milestones := make([]int64, 0, 8)
	seenEpic, seenMilestone := map[string]bool{}, map[int64]bool{}
	for _, bar := range bars {
		if bar.Epic != "" && !seenEpic[bar.Epic] {
			seenEpic[bar.Epic] = true
			epics = append(epics, bar.Epic)
		}
		if bar.MilestoneID > 0 && !seenMilestone[bar.MilestoneID] {
			seenMilestone[bar.MilestoneID] = true
			milestones = append(milestones, bar.MilestoneID)
		}
	}
	slices.Sort(epics)
	slices.Sort(milestones)

	rows := make([]planning_service.SpanRow, 0, len(epics)+len(milestones))
	for _, epic := range epics {
		row, held, ok, err := timelineRollup(ctx, repo, state, "epic", epic, func(opts *issues_model.IssuesOptions) {
			opts.IncludedLabelNames = []string{planning_service.EpicLabelPrefix + epic}
		})
		if err != nil {
			return nil, err
		}
		if ok {
			rows = append(rows, row)
			children.collect(row, held)
		}
	}
	for _, milestoneID := range milestones {
		row, held, ok, err := timelineRollup(ctx, repo, state, "milestone", strconv.FormatInt(milestoneID, 10),
			func(opts *issues_model.IssuesOptions) { opts.MilestoneIDs = []int64{milestoneID} })
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

// timelineMilestoneRollups pages over the repository's milestones rather than its issues, so a
// page of N holds N milestone rows and truncated means more milestones than the page holds.
func timelineMilestoneRollups(ctx *context.APIContext, repo *repo_model.Repository, opts *issues_model.IssuesOptions, limit int, children *rolledChildren) ([]planning_service.SpanRow, bool, error) {
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
	rows := make([]planning_service.SpanRow, 0, len(milestones))
	for _, milestone := range milestones {
		// The milestone_id filter narrows the chart here, because this zoom's page is over
		// milestones rather than over the issues it would otherwise have narrowed.
		if len(opts.MilestoneIDs) > 0 && !slices.Contains(opts.MilestoneIDs, milestone.ID) {
			continue
		}
		row, held, ok, err := timelineRollup(ctx, repo, opts.IsClosed, "milestone", strconv.FormatInt(milestone.ID, 10),
			func(o *issues_model.IssuesOptions) { o.MilestoneIDs = []int64{milestone.ID} })
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

// timelineRollup folds one parent's children into its row, one query per parent because a
// child's window cannot be resolved in SQL. ok=false for a parent whose children draw no bar.
func timelineRollup(ctx *context.APIContext, repo *repo_model.Repository, state optional.Option[bool], kind, key string, narrow func(*issues_model.IssuesOptions)) (planning_service.SpanRow, issues_model.IssueList, bool, error) {
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
		return planning_service.SpanRow{}, nil, false, err
	}
	if err := issues.LoadAttributes(ctx); err != nil {
		return planning_service.SpanRow{}, nil, false, err
	}
	starts, err := ccpmStarts(ctx, issues)
	if err != nil {
		return planning_service.SpanRow{}, nil, false, err
	}
	children := make([]planning_service.Bar, 0, len(issues))
	for _, issue := range issues {
		if bar, ok := planning_service.ResolveBar(barInputFor(issue, starts[issue.ID])); ok {
			children = append(children, bar)
		}
	}

	for _, row := range planning_service.BuildSpans(children) {
		if row.Kind != kind || row.Key != key {
			continue
		}
		if len(issues) >= query.MaxLimit {
			row.MarkPartial()
		}
		return row, issues, true, nil
	}
	return planning_service.SpanRow{}, nil, false, nil
}

// timelineRepo resolves and authorizes the repository the chart covers.
func timelineRepo(ctx *context.APIContext, repoID int64) (*repo_model.Repository, access.Permission, bool) {
	var perm access.Permission
	if repoID <= 0 {
		hubapi.APIError(ctx, http.StatusBadRequest, "missing_repo_id",
			"repo_id is required: a timeline covers one repository's issues",
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

// timelineStates is the accepted set for ?state=.
var timelineStates = []string{"open", "closed", "all"}

func parseTimelineState(ctx *context.APIContext, raw string) (optional.Option[bool], bool) {
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
		Accepted:        timelineStates,
		SuggestedAction: "Use state=open, state=closed or state=all; all is the default, because a chart that hid closed work would show no finished bar.",
	})
	return optional.None[bool](), false
}

// barInputFor reduces one issue to what bar resolution depends on.
func barInputFor(issue *issues_model.Issue, startedUnix int64) planning_service.BarInput {
	in := planning_service.BarInput{
		IssueID: issue.ID, Number: issue.Index, Title: issue.Title, URL: issue.Link(),
		StartedUnix:   startedUnix,
		CreatedUnix:   int64(issue.CreatedUnix),
		ClosedUnix:    int64(issue.ClosedUnix),
		DeadlineUnix:  int64(issue.DeadlineUnix),
		EffortSeconds: planning_service.ParseEffortSeconds(issue.Content),
		IsClosed:      issue.IsClosed,
		Labels:        make([]string, 0, len(issue.Labels)),
		Assignees:     make([]string, 0, len(issue.Assignees)),
	}
	for _, label := range issue.Labels {
		in.Labels = append(in.Labels, label.Name)
		if in.Epic == "" && strings.HasPrefix(label.Name, planning_service.EpicLabelPrefix) &&
			len(label.Name) > len(planning_service.EpicLabelPrefix) {
			// Managed means ccpm files it under an epic, which is the only thing that
			// gives the issue a start to draw.
			in.Managed = true
			in.Epic = label.Name[len(planning_service.EpicLabelPrefix):]
		}
	}
	for _, assignee := range issue.Assignees {
		in.Assignees = append(in.Assignees, assignee.Name)
	}
	if issue.Milestone != nil {
		in.MilestoneID, in.Milestone = issue.Milestone.ID, issue.Milestone.Name
	}
	return in
}

// ccpmStarts reads the ccpm:started marker off each issue's comments.
//
// The marker is the ONLY carrier of a start date onto the forge: Gitea has no column for
// one, and the comment is append-only. The LAST marker posted wins, because it is the most
// recent statement of when the work started: re-syncing an unchanged value changes nothing,
// a changed one is ccpm correcting itself, and dragging the chart's start edge later has to
// move the bar or the edge only drags one way.
func ccpmStarts(ctx *context.APIContext, issues issues_model.IssueList) (map[int64]int64, error) {
	starts := map[int64]int64{}
	if len(issues) == 0 {
		return starts, nil
	}
	ids := make([]int64, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}

	comments := make([]*issues_model.Comment, 0, len(ids))
	if err := db.GetEngine(ctx).
		Where(builder.Eq{"type": issues_model.CommentTypeComment}).
		In("issue_id", ids).
		OrderBy("created_unix ASC, id ASC").
		Find(&comments); err != nil {
		return nil, err
	}

	for _, comment := range comments {
		marker := planning_service.ParseStartedMarker(comment.Content)
		if marker == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, marker)
		if err != nil {
			// A malformed marker is not an error the whole chart should fail on: the bar
			// falls back to the issue's creation time and says so.
			continue
		}
		// Comments are ordered oldest first, so the last assignment is the newest marker.
		starts[comment.IssueID] = at.Unix()
	}
	return starts, nil
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
		return nil, errors.New("depends_on is not a relation the timeline can draw")
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

// timelineArrows attaches the page's edges to the bars it drew. An edge whose other end is
// not drawn is dropped: an arrow to a bar that is not on the chart would point at nothing.
func timelineArrows(ctx *context.APIContext, issues issues_model.IssueList, drawn map[int64]bool) ([]planning_service.Arrow, error) {
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
	kind   string // only rows of this kind are on the page, so only they collect
	span   map[int64]string
	issues issues_model.IssueList
}

func newRolledChildren(kind string) *rolledChildren {
	return &rolledChildren{kind: kind, span: map[int64]string{}}
}

func (c *rolledChildren) collect(row planning_service.SpanRow, issues issues_model.IssueList) {
	if c == nil || row.Kind != c.kind {
		return
	}
	for _, issue := range issues {
		if _, seen := c.span[issue.ID]; seen {
			continue
		}
		c.span[issue.ID] = row.SpanKey()
		c.issues = append(c.issues, issue)
	}
}

// timelineRolledArrows re-keys the edges among a page's rollup children onto the brackets
// that hold them, so an edge whose end is inside a bracket attaches to the bracket instead of
// vanishing with the bar it pointed at. The gate/sequence distinction survives the re-keying.
func timelineRolledArrows(ctx *context.APIContext, children *rolledChildren) ([]planning_service.Arrow, error) {
	edges, err := readArrowEdges(ctx, children.issues)
	if err != nil {
		return nil, err
	}
	arrows := make([]planning_service.Arrow, 0, len(edges))
	seen := map[string]bool{}
	for _, edge := range edges {
		from, to := children.span[edge.from], children.span[edge.to]
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
		arrow.FromSpan, arrow.ToSpan = from, to
		arrows = append(arrows, arrow)
	}
	return arrows, nil
}
