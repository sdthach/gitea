// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The roadmap draws one bar per issue with dependency arrows. It needs no
// Projects API, so it renders on a build the board cannot.
//
// Gitea stores NO start date: models/issues/issue.go declares DeadlineUnix and ClosedUnix
// and no started field, and models/issues/milestone.go the same. A bar therefore needs a
// start from somewhere else, and every bar states which source produced each of its
// endpoints. Presenting an estimated bar as a measured one is this view's
// characteristic failure, so an inferred end is flagged rather than merely rendered.
//
// Everything in this file is pure. The API handler reads the rows; this decides the shape.

// StartSource names where a bar's start came from.
type StartSource string

const (
	// StartFromSchedule is a recorded row in plan_issue_schedule, written through
	// services/planning's own SetIssueStart rather than inferred from anything else.
	StartFromSchedule StartSource = "schedule"
	// StartFromCreated is the issue's creation time, the fallback start.
	StartFromCreated StartSource = "issue_created"
	// StartNone is an issue ccpm does not manage: it has no start to draw.
	StartNone StartSource = "none"
)

// EndSource names where a bar's end came from.
type EndSource string

const (
	// EndFromClosed is the close time: recorded.
	EndFromClosed EndSource = "closed"
	// EndFromDeadline is Issue.DeadlineUnix: recorded.
	EndFromDeadline EndSource = "deadline"
	// EndFromEstimate is the effort estimate applied to the start: INFERRED.
	EndFromEstimate EndSource = "effort_estimate"
)

// StartSources and EndSources are the published enumerations, so the document, the CLI and
// the page all read one list.
var (
	StartSources = []string{string(StartFromSchedule), string(StartFromCreated), string(StartNone)}
	EndSources   = []string{string(EndFromClosed), string(EndFromDeadline), string(EndFromEstimate)}
)

// Inferred reports whether an end source is an estimate rather than a record.
func (s EndSource) Inferred() bool { return s == EndFromEstimate }

// daySeconds is the unit effort sizes are expressed in.
const daySeconds = int64(24 * 60 * 60)

// EffortDays maps ccpm's effort sizes onto a duration. The sizes are the ones
// references/issue-types.yaml puts in the Effort Estimate placeholder; the days are this
// view's own reading of them and exist only to give an unfinished task a width.
var EffortDays = map[string]int64{"xs": 1, "s": 2, "m": 5, "l": 10, "xl": 20}

// DefaultEffortDays is the width of a bar whose issue states no estimate. It is deliberately
// the middle size rather than zero: a zero-width bar would read as an instant task.
const DefaultEffortDays = int64(5)

// effortLine reads the size token out of an Effort Estimate or Timebox line.
var effortLine = regexp.MustCompile(`(?i)^[-*\s]*(?:size|timebox|effort)\s*:\s*([A-Za-z0-9.]+)`)

// durationLiteral is a bare `3d` / `4h` / `90m` estimate.
var durationLiteral = regexp.MustCompile(`^(\d+(?:\.\d+)?)([hdwm])$`)

// ParseEffortSeconds reads an effort estimate out of an issue body, in the shape
// references/issue-types.yaml renders it: an `### Effort Estimate` section holding
// `- Size: M`, or a spike's `### Timebox` holding `4h`.
//
// It returns the default width, not zero, for a body that states nothing: a bar has to have
// a width, and the source label is what tells the reader it was inferred.
func ParseEffortSeconds(body string) int64 {
	for line := range strings.SplitSeq(body, "\n") {
		m := effortLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if seconds, ok := effortToken(m[1]); ok {
			return seconds
		}
	}
	return DefaultEffortDays * daySeconds
}

func effortToken(token string) (int64, bool) {
	token = strings.TrimSpace(strings.ToLower(token))
	if days, ok := EffortDays[token]; ok {
		return days * daySeconds, true
	}
	if m := durationLiteral.FindStringSubmatch(token); m != nil {
		value, err := strconv.ParseFloat(m[1], 64)
		if err != nil || value <= 0 {
			return 0, false
		}
		unit := map[string]int64{"h": 3600, "d": daySeconds, "w": 7 * daySeconds, "m": 60}[m[2]]
		return int64(value * float64(unit)), true
	}
	return 0, false
}

// BarInput is one issue reduced to what bar resolution depends on.
type BarInput struct {
	IssueID int64
	Number  int64
	Title   string
	URL     string
	// Managed is whether ccpm manages the issue: it carries an epic:<name> label. An
	// unmanaged issue gets no bar and one stated reason.
	Managed bool
	Epic    string
	// Type is ccpm's type:<t> value. Empty means the caller has not resolved one and
	// ResolveBar reads it off Labels.
	Type string
	// Labels and Assignees are what group assignment reads, so the chart groups by the same
	// definition the board does.
	Labels    []string
	Assignees []string
	// ScheduledStartUnix is the recorded plan_issue_schedule row, 0 when the issue has none.
	ScheduledStartUnix int64
	CreatedUnix        int64
	ClosedUnix         int64
	DeadlineUnix       int64
	// EffortSeconds is the parsed estimate, used only when nothing recorded an end.
	EffortSeconds int64
	IsClosed      bool
	MilestoneID   int64
	Milestone     string
}

// Bar is one row of the chart.
type Bar struct {
	IssueID     int64  `json:"issue_id"`
	Number      int64  `json:"number"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Epic        string `json:"epic,omitempty"`
	Type        string `json:"type,omitempty"`
	Milestone   string `json:"milestone,omitempty"`
	MilestoneID int64  `json:"milestone_id,omitempty"`
	StartUnix   int64  `json:"start_unix"`
	EndUnix     int64  `json:"end_unix"`
	// Labels and Assignees carry group assignment onto the chart, so a vertical drag and a
	// board group move write the same field.
	Labels    []string `json:"labels,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	// StartSource and EndSource are on every bar: declaring where an endpoint came from is
	// the point of the view rather than a detail of it.
	StartSource StartSource `json:"start_source"`
	EndSource   EndSource   `json:"end_source"`
	// EndInferred is EndSource.Inferred(), published so a client renders the distinction
	// without knowing the enumeration.
	EndInferred bool `json:"end_inferred"`
	IsClosed    bool `json:"is_closed"`
}

// Unmanaged is an issue with no bar, listed beside the chart with the reason.
type Unmanaged struct {
	IssueID int64  `json:"issue_id"`
	Number  int64  `json:"number"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Reason  string `json:"reason"`
	// SuggestedAction is what to do about it, like every other refusal.
	SuggestedAction string `json:"suggested_action"`
}

// ResolveBar decides one bar's endpoints and names the source of each.
//
// It returns ok=false for an issue ccpm does not manage: that issue has no start to draw and
// is listed with its reason instead of being given a fabricated bar.
func ResolveBar(in BarInput) (Bar, bool) {
	if !in.Managed {
		return Bar{}, false
	}

	bar := Bar{
		IssueID: in.IssueID, Number: in.Number, Title: in.Title, URL: in.URL,
		Epic: in.Epic, Milestone: in.Milestone, MilestoneID: in.MilestoneID,
		Type: in.Type, Labels: in.Labels, Assignees: in.Assignees,
		IsClosed: in.IsClosed,
	}
	if bar.Type == "" {
		bar.Type = labelValue(in.Labels, TypeLabelPrefix)
	}

	switch {
	case in.ScheduledStartUnix > 0:
		bar.StartUnix, bar.StartSource = in.ScheduledStartUnix, StartFromSchedule
	default:
		bar.StartUnix, bar.StartSource = in.CreatedUnix, StartFromCreated
	}

	// The close time when closed, the deadline when set, otherwise the effort estimate
	// applied to the start.
	switch {
	case in.IsClosed && in.ClosedUnix > 0:
		bar.EndUnix, bar.EndSource = in.ClosedUnix, EndFromClosed
	case in.DeadlineUnix > 0:
		bar.EndUnix, bar.EndSource = in.DeadlineUnix, EndFromDeadline
	default:
		effort := in.EffortSeconds
		if effort <= 0 {
			effort = DefaultEffortDays * daySeconds
		}
		bar.EndUnix, bar.EndSource = bar.StartUnix+effort, EndFromEstimate
	}

	// A recorded end that predates the start is real data and keeps its source; the bar is
	// clamped rather than drawn backwards.
	if bar.EndUnix < bar.StartUnix {
		bar.EndUnix = bar.StartUnix
	}
	bar.EndInferred = bar.EndSource.Inferred()
	return bar, true
}

// UnmanagedFor states why an issue has no bar, and what to do about it.
func UnmanagedFor(in BarInput) Unmanaged {
	return Unmanaged{
		IssueID: in.IssueID, Number: in.Number, Title: in.Title, URL: in.URL,
		Reason: "ccpm does not manage this issue: it carries no " + EpicLabelPrefix + "<name> label, so there is no start to draw",
		SuggestedAction: "Import it into an epic, or add an " + EpicLabelPrefix + "<name> label to it. " +
			"A bar drawn from creation alone would present a guess as a schedule.",
	}
}

// ArrowKind distinguishes a hard gate from a sequencing hint. They do not read the
// same on a schedule: one is enforced by the forge, the other is advice.
type ArrowKind string

const (
	// ArrowGate is depends_on / blocked-by: written to issue_dependency, and Gitea itself
	// refuses to close the blocked issue.
	ArrowGate ArrowKind = "depends_on"
	// ArrowSequence is predecessor / successor: recorded as a rendered cross-reference and
	// enforced by nothing.
	ArrowSequence ArrowKind = "predecessor"
)

// ArrowKinds is the published set.
var ArrowKinds = []string{string(ArrowGate), string(ArrowSequence)}

// ArrowKindFor maps a relation word onto the arrow it draws. A word in the vocabulary that
// carries no ordering — related, duplicate-of, caused-by, parent — draws no arrow at all,
// because it says nothing about a schedule.
func ArrowKindFor(relation string) (ArrowKind, bool) {
	switch strings.TrimSpace(strings.ToLower(relation)) {
	case "depends_on", "blocked-by", "blocked_by", "blocks":
		return ArrowGate, true
	case "predecessor", "successor":
		return ArrowSequence, true
	}
	return "", false
}

// Arrow is one dependency edge between two bars.
type Arrow struct {
	// FromIssueID is the issue that must come first.
	FromIssueID int64     `json:"from_issue_id"`
	ToIssueID   int64     `json:"to_issue_id"`
	Kind        ArrowKind `json:"kind"`
	// Enforced is whether the forge itself acts on the edge, which is the whole
	// difference between the two kinds.
	Enforced bool `json:"enforced"`
	// FromRollup and ToRollup name the brackets an edge joins at a rolled-up zoom, as
	// kind:key. Empty at issue zoom, where the edge already joins two drawn bars.
	FromRollup string `json:"from_rollup,omitempty"`
	ToRollup   string `json:"to_rollup,omitempty"`
}

// NewArrow builds an edge, filling in whether the forge enforces it.
func NewArrow(from, to int64, kind ArrowKind) Arrow {
	return Arrow{FromIssueID: from, ToIssueID: to, Kind: kind, Enforced: kind == ArrowGate}
}

// relationLine matches the cross-reference lines ccpm renders into an issue body under its
// Relations heading: "Predecessor #12", "Successor #13".
var relationLine = regexp.MustCompile(`(?i)^(predecessor|successor)\s+#(\d+)\s*$`)

// ParseSequenceRelations reads the sequencing edges out of an issue body. The enforced edges
// are deliberately NOT read from here: they live in issue_dependency, and reading them from
// the body too would make the roadmap unable to say which source an edge came from.
//
// It returns (word, number) pairs in the order they appear.
func ParseSequenceRelations(body string) [][2]string {
	out := make([][2]string, 0, 2)
	for line := range strings.SplitSeq(body, "\n") {
		if m := relationLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			out = append(out, [2]string{strings.ToLower(m[1]), m[2]})
		}
	}
	return out
}

// RollupRow is an epic or milestone row: it rolls up the earliest start to the latest end of its
// children, and its progress is ccpm's existing task-close percentage. No second definition
// of progress is introduced.
type RollupRow struct {
	Kind      string `json:"kind"` // "epic" or "milestone"
	Key       string `json:"key"`
	Label     string `json:"label"`
	StartUnix int64  `json:"start_unix"`
	EndUnix   int64  `json:"end_unix"`
	Children  int    `json:"children"`
	Closed    int    `json:"closed"`
	// Progress is closed children over all children, as a whole percentage. It is 0 on a
	// partial row: a fraction of an unknown denominator is not a measurement.
	Progress int `json:"progress"`
	// EndInferred is true when ANY child's end is inferred, so a rollup is never drawn as
	// firmer than the bars it is made of.
	EndInferred bool `json:"end_inferred"`
	// Partial is true when the fetch behind this row hit its cap, so the row covers a
	// prefix of the parent's children rather than all of them.
	Partial bool `json:"partial"`
	// IssueID is the type:epic issue the key names, so a bracket can be opened. 0 for a
	// milestone, which is not an issue.
	IssueID int64 `json:"issue_id,omitempty"`
	// DeclaredStartUnix and DeclaredEndUnix are the epic issue's OWN bar, which is a
	// different window from the one its children derive.
	DeclaredStartUnix int64 `json:"declared_start_unix,omitempty"`
	DeclaredEndUnix   int64 `json:"declared_end_unix,omitempty"`
	// ContainsChildren is whether the declared window contains the derived one. A row with
	// no declared window contains its children vacuously and carries no warning.
	ContainsChildren bool   `json:"contains_children"`
	Warning          string `json:"warning,omitempty"`
	SuggestedAction  string `json:"suggested_action,omitempty"`
}

// Zoom is the depth the chart is read at. It is a view setting, never stored.
type Zoom string

const (
	// ZoomIssue draws one bar per issue, which is the chart's own level.
	ZoomIssue Zoom = "issue"
	// ZoomEpic draws only epic rollups.
	ZoomEpic Zoom = "epic"
	// ZoomMilestone draws only milestone rollups.
	ZoomMilestone Zoom = "milestone"
)

// Zooms is the accepted set. The API refuses anything else naming this list.
var Zooms = []string{string(ZoomIssue), string(ZoomEpic), string(ZoomMilestone)}

// TypeEpic is the type: value ccpm puts on an epic's own issue, beside the epic:<name>
// label it also puts there.
const TypeEpic = "epic"

// ParseZoom resolves a caller's word. An empty string means issue, so a chart with no zoom
// parameter draws bars rather than being refused.
func ParseZoom(s string) (Zoom, bool) {
	switch Zoom(strings.TrimSpace(strings.ToLower(s))) {
	case "", ZoomIssue:
		return ZoomIssue, true
	case ZoomEpic:
		return ZoomEpic, true
	case ZoomMilestone:
		return ZoomMilestone, true
	}
	return ZoomIssue, false
}

// BuildRollup folds a set of bars into one row. A rollup over no bars is not a row: it returns
// ok=false rather than a zero-width bar at the epoch.
func BuildRollup(kind, key, label string, bars []Bar) (RollupRow, bool) {
	if len(bars) == 0 {
		return RollupRow{}, false
	}
	row := RollupRow{Kind: kind, Key: key, Label: label, StartUnix: bars[0].StartUnix, EndUnix: bars[0].EndUnix}
	for _, bar := range bars {
		if bar.StartUnix < row.StartUnix {
			row.StartUnix = bar.StartUnix
		}
		if bar.EndUnix > row.EndUnix {
			row.EndUnix = bar.EndUnix
		}
		if bar.IsClosed {
			row.Closed++
		}
		if bar.EndInferred {
			row.EndInferred = true
		}
	}
	row.Children = len(bars)
	row.Progress = row.Closed * 100 / row.Children
	return row, true
}

// BuildRollups folds bars into epic and milestone rows, epics first, each set ordered by key.
// An epic's own issue is its declared window, not one of its children; milestones count it.
func BuildRollups(bars []Bar) []RollupRow {
	byEpic := map[string][]Bar{}
	declared := map[string]Bar{}
	byMilestone := map[string][]Bar{}
	milestoneLabel := map[string]string{}
	for _, bar := range bars {
		switch {
		case bar.Epic == "":
		case bar.Type == TypeEpic:
			declared[bar.Epic] = bar
			if _, seen := byEpic[bar.Epic]; !seen {
				byEpic[bar.Epic] = nil // an epic with no child is still an epic
			}
		default:
			byEpic[bar.Epic] = append(byEpic[bar.Epic], bar)
		}
		if bar.MilestoneID > 0 {
			key := strconv.FormatInt(bar.MilestoneID, 10)
			byMilestone[key] = append(byMilestone[key], bar)
			milestoneLabel[key] = bar.Milestone
		}
	}

	rows := make([]RollupRow, 0, len(byEpic)+len(byMilestone))
	for _, key := range sortedKeys(byEpic) {
		row, ok := BuildRollup("epic", key, key, byEpic[key])
		if !ok {
			// A freshly filed epic has a window and no children yet; drawing nothing for
			// it would say nothing about it.
			if declared[key].IssueID == 0 {
				continue
			}
			row = RollupRow{
				Kind: "epic", Key: key, Label: key,
				StartUnix: declared[key].StartUnix, EndUnix: declared[key].EndUnix,
				EndInferred: declared[key].EndInferred,
			}
		}
		applyContainment(&row, declared[key], byEpic[key])
		rows = append(rows, row)
	}
	for _, key := range sortedKeys(byMilestone) {
		if row, ok := BuildRollup("milestone", key, milestoneLabel[key], byMilestone[key]); ok {
			row.ContainsChildren = true
			rows = append(rows, row)
		}
	}
	return rows
}

// RollupKey names a rollup as kind:key, which is what an arrow attaches to when the row it
// points at is a bracket rather than a bar.
func (r RollupRow) RollupKey() string { return r.Kind + ":" + r.Key }

// MarkPartial withdraws the progress figure: a fraction of an unknown denominator is not a
// measurement.
func (r *RollupRow) MarkPartial() {
	r.Partial, r.Progress = true, 0
}

// applyContainment warns when an epic's declared window does not contain its children's.
// Containment, not a sum of effort: children run in parallel, so a sum warns on every plan.
func applyContainment(row *RollupRow, declared Bar, children []Bar) {
	row.ContainsChildren = true
	if declared.IssueID == 0 {
		return
	}
	row.IssueID = declared.IssueID
	row.DeclaredStartUnix, row.DeclaredEndUnix = declared.StartUnix, declared.EndUnix
	if len(children) == 0 {
		return
	}

	var warnings, actions []string
	if declared.EndUnix < row.EndUnix {
		warnings = append(warnings, fmt.Sprintf("epic %s (#%d) ends %s before the work filed under it",
			row.Key, declared.Number, days(row.EndUnix-declared.EndUnix)))
		actions = append(actions, fmt.Sprintf("Move the epic's deadline to %s, or move %s earlier.",
			utcDay(row.EndUnix), nameOf(latest(children, func(bar Bar) int64 { return -bar.EndUnix }))))
	}
	if declared.StartUnix > row.StartUnix {
		warnings = append(warnings, fmt.Sprintf("epic %s (#%d) starts %s after the work filed under it",
			row.Key, declared.Number, days(declared.StartUnix-row.StartUnix)))
		actions = append(actions, fmt.Sprintf("Move the epic's start to %s, or move %s later.",
			utcDay(row.StartUnix), nameOf(latest(children, func(bar Bar) int64 { return bar.StartUnix }))))
	}
	if len(warnings) == 0 {
		return
	}
	row.ContainsChildren = false
	row.Warning = strings.Join(warnings, "; ")
	row.SuggestedAction = strings.Join(actions, " ")
}

// latest returns the bar with the smallest rank: the one child a warning names.
func latest(bars []Bar, rank func(Bar) int64) Bar {
	pick := bars[0]
	for _, bar := range bars[1:] {
		if rank(bar) < rank(pick) {
			pick = bar
		}
	}
	return pick
}

// nameOf points the suggested action at one issue to move.
func nameOf(bar Bar) string {
	kind := bar.Type
	if kind == "" {
		kind = "issue"
	}
	return kind + " #" + strconv.FormatInt(bar.Number, 10)
}

// days rounds up, so an overhang shorter than a day still reads as one rather than none.
func days(seconds int64) string {
	count := (seconds + daySeconds - 1) / daySeconds
	if count == 1 {
		return "1 day"
	}
	return strconv.FormatInt(count, 10) + " days"
}

func utcDay(unix int64) string { return time.Unix(unix, 0).UTC().Format(time.DateOnly) }

func sortedKeys(m map[string][]Bar) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
