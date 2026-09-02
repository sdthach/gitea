// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	"gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/optional"
	"gitea.dev/services/context"
	delivery_service "gitea.dev/services/delivery"
	"gitea.dev/services/delivery/query"

	"xorm.io/builder"
)

// timelineSpec is the timeline projection's whitelist declaration. Like the grid and the
// board it is a projection rather than a table, so its parameters select what to project;
// they still go through the one grammar, so an unknown one is refused (I2, I4).
var timelineSpec = query.Spec{
	Resource: "timeline",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "epic", Column: "epic", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
		{Name: "milestone_id", Column: "milestone_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "state", Column: "state", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
	},
	PrimaryKey: "issue_id",
	Paging:     query.PagingOffset,
}

// Timeline is the timeline resource's response shape.
type Timeline struct {
	RepoID       int64                        `json:"repo_id"`
	RepoFullName string                       `json:"repo_full_name"`
	Bars         []delivery_service.Bar       `json:"bars"`
	Arrows       []delivery_service.Arrow     `json:"arrows"`
	Spans        []delivery_service.SpanRow   `json:"spans"`
	Unmanaged    []delivery_service.Unmanaged `json:"unmanaged"`
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

func getTimelineEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "getTimeline", Method: http.MethodGet, Path: "/timeline",
			Summary: "The delivery timeline: one bar per issue, with dependency arrows",
			Description: "Needs no Projects API, so it renders on a build the board cannot (O6, SC 38). " +
				"Gitea stores no start date — Issue.DeadlineUnix is a single endpoint — so a bar's start comes from ccpm: " +
				"the `started:` in updates/<N>/progress.md, carried onto the issue by issue-sync as a `ccpm:started=` marker " +
				"on the progress comment, falling back to the issue's creation time. Its end is the close time when closed, " +
				"the deadline when set, and otherwise the effort estimate applied to the start (O7). " +
				"EVERY bar names the source of its start and of its end, and an inferred end is flagged, because presenting " +
				"an estimate as a measurement is this view's characteristic failure (O8). " +
				"Arrows distinguish depends_on, which Gitea's issue_dependency enforces, from predecessor, which is a " +
				"sequencing hint enforced by nothing (O9, N9). " +
				"An issue ccpm does not manage has no start to draw: it is listed with that reason rather than given a " +
				"fabricated bar (O10). Epic and milestone rows span earliest start to latest end of their children, and " +
				"their progress is ccpm's own task-close percentage; no second definition is introduced (O11). " +
				"Scoped by Gitea's own permission check on the Issues unit (E12, I13). " +
				"The /delivery/timeline page is a client of this endpoint (E18, I14).",
			Tag: "timeline", Query: &timelineSpec, Response: "Timeline", ResponseIs: "object",
		},
		Handler: GetTimeline,
	}
}

// GetTimeline answers GET /timeline.
func GetTimeline(ctx *context.APIContext) {
	q, ok := parseQuery(ctx, timelineSpec)
	if !ok {
		return
	}
	repo, perm, ok := timelineRepo(ctx, equalityFilterInt(q, "repo_id"))
	if !ok {
		return
	}

	state, ok := parseTimelineState(ctx, equalityFilterString(q, "state"))
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
	if epic := equalityFilterString(q, "epic"); epic != "" {
		opts.IncludedLabelNames = []string{delivery_service.EpicLabelPrefix + epic}
	}
	if milestoneID := equalityFilterInt(q, "milestone_id"); milestoneID > 0 {
		opts.MilestoneIDs = []int64{milestoneID}
	}

	renderTimeline(ctx, repo, opts, q.Limit, perm.CanWrite(unit.TypeIssues))
}

// renderTimeline projects one repository's issues and answers with the chart. Every write
// endpoint replies through it, so a caller never has to re-fetch to see what its write did,
// and the chart it gets back is the one GET would have produced.
func renderTimeline(ctx *context.APIContext, repo *repo_model.Repository, opts *issues_model.IssuesOptions, limit int, canWrite bool) {
	issues, err := issues_model.Issues(ctx, opts)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if err := issues.LoadAttributes(ctx); err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	starts, err := ccpmStarts(ctx, issues)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	out := &Timeline{
		RepoID: repo.ID, RepoFullName: repo.FullName(),
		Bars: []delivery_service.Bar{}, Arrows: []delivery_service.Arrow{},
		Spans: []delivery_service.SpanRow{}, Unmanaged: []delivery_service.Unmanaged{},
		Rows: []TimelineRow{}, CanWrite: canWrite,
		Truncated: len(issues) == limit,
	}

	milestones, err := db.Find[issues_model.Milestone](ctx, issues_model.FindMilestoneOptions{RepoID: repo.ID})
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	for _, m := range milestones {
		out.Rows = append(out.Rows, TimelineRow{MilestoneID: m.ID, Title: m.Name, IsClosed: m.IsClosed})
	}

	byNumber := make(map[int64]int64, len(issues))
	drawn := make(map[int64]bool, len(issues))
	for _, issue := range issues {
		in := barInputFor(issue, starts[issue.ID])
		byNumber[issue.Index] = issue.ID
		if bar, ok := delivery_service.ResolveBar(in); ok {
			out.Bars = append(out.Bars, bar)
			drawn[issue.ID] = true
			continue
		}
		// O10: listed beside the chart with the reason, never given a fabricated bar.
		out.Unmanaged = append(out.Unmanaged, delivery_service.UnmanagedFor(in))
	}

	arrows, err := timelineArrows(ctx, issues, byNumber, drawn)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	out.Arrows = arrows
	out.Spans = delivery_service.BuildSpans(out.Bars)
	ctx.JSON(http.StatusOK, out)
}

// timelineRepo resolves and authorizes the repository the chart covers.
func timelineRepo(ctx *context.APIContext, repoID int64) (*repo_model.Repository, access.Permission, bool) {
	var perm access.Permission
	if repoID <= 0 {
		apiError(ctx, http.StatusBadRequest, "missing_repo_id",
			"repo_id is required: a timeline covers one repository's issues",
			"Pass ?repo_id=<id>, listing "+BasePath+"/repos to find it.")
		return nil, perm, false
	}
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		apiError(ctx, http.StatusNotFound, "repo_not_found",
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
		apiError(ctx, http.StatusForbidden, "forbidden",
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
func barInputFor(issue *issues_model.Issue, startedUnix int64) delivery_service.BarInput {
	in := delivery_service.BarInput{
		IssueID: issue.ID, Number: issue.Index, Title: issue.Title, URL: issue.Link(),
		StartedUnix:   startedUnix,
		CreatedUnix:   int64(issue.CreatedUnix),
		ClosedUnix:    int64(issue.ClosedUnix),
		DeadlineUnix:  int64(issue.DeadlineUnix),
		EffortSeconds: delivery_service.ParseEffortSeconds(issue.Content),
		IsClosed:      issue.IsClosed,
	}
	for _, label := range issue.Labels {
		if len(label.Name) > len(delivery_service.EpicLabelPrefix) &&
			label.Name[:len(delivery_service.EpicLabelPrefix)] == delivery_service.EpicLabelPrefix {
			// Managed means ccpm files it under an epic, which is the only thing that
			// gives the issue a start to draw (O10).
			in.Managed = true
			in.Epic = label.Name[len(delivery_service.EpicLabelPrefix):]
			break
		}
	}
	if issue.Milestone != nil {
		in.MilestoneID, in.Milestone = issue.Milestone.ID, issue.Milestone.Name
	}
	return in
}

// ccpmStarts reads the ccpm:started marker off each issue's comments.
//
// The marker is the ONLY carrier of a start date onto the forge: Gitea has no column for
// one. The earliest marker wins, so a re-synced progress comment does not move a bar that
// already started.
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
		marker := delivery_service.ParseStartedMarker(comment.Content)
		if marker == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, marker)
		if err != nil {
			// A malformed marker is not an error the whole chart should fail on: the bar
			// falls back to the issue's creation time and says so.
			continue
		}
		if existing, seen := starts[comment.IssueID]; seen && existing <= at.Unix() {
			continue
		}
		starts[comment.IssueID] = at.Unix()
	}
	return starts, nil
}

// timelineArrows builds the dependency edges, from the two sources they actually live in
// (N8): issue_dependency for the enforced ones, and the rendered cross-reference lines in
// the body for the sequencing ones.
//
// An edge whose other end is not drawn is dropped: an arrow to a bar that is not on the
// chart would point at nothing.
func timelineArrows(ctx *context.APIContext, issues issues_model.IssueList, byNumber map[int64]int64, drawn map[int64]bool) ([]delivery_service.Arrow, error) {
	arrows := make([]delivery_service.Arrow, 0, 8)
	seen := map[string]bool{}
	add := func(from, to int64, kind delivery_service.ArrowKind) {
		if from == 0 || to == 0 || from == to || !drawn[from] || !drawn[to] {
			return
		}
		key := strconv.FormatInt(from, 10) + ">" + strconv.FormatInt(to, 10) + ":" + string(kind)
		if seen[key] {
			return
		}
		seen[key] = true
		arrows = append(arrows, delivery_service.NewArrow(from, to, kind))
	}

	ids := make([]int64, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	if len(ids) > 0 {
		deps := make([]*issues_model.IssueDependency, 0, len(ids))
		if err := db.GetEngine(ctx).In("issue_id", ids).Find(&deps); err != nil {
			return nil, err
		}
		// A row in issue_dependency IS the depends_on relation (N1, N3), so its arrow kind
		// comes from the same mapping the body-derived words go through.
		gate, ok := delivery_service.ArrowKindFor("depends_on")
		if !ok {
			return nil, errors.New("depends_on is not a relation the timeline can draw")
		}
		for _, dep := range deps {
			// DependencyID blocks IssueID, so the blocker comes first on a schedule.
			add(dep.DependencyID, dep.IssueID, gate)
		}
	}

	for _, issue := range issues {
		for _, rel := range delivery_service.ParseSequenceRelations(issue.Content) {
			number, err := strconv.ParseInt(rel[1], 10, 64)
			if err != nil {
				continue
			}
			// The word decides the arrow: a vocabulary word that carries no ordering
			// draws nothing at all (N2, O9).
			kind, ok := delivery_service.ArrowKindFor(rel[0])
			if !ok {
				continue
			}
			other := byNumber[number]
			if rel[0] == "predecessor" {
				add(other, issue.ID, kind)
			} else {
				add(issue.ID, other, kind)
			}
		}
	}
	return arrows, nil
}
