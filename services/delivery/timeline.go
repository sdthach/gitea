// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The delivery timeline draws one bar per issue with dependency arrows (O6). It needs no
// Projects API, so it renders on a build the board cannot (SC 38).
//
// Gitea stores NO start date: models/issues/issue.go declares DeadlineUnix and ClosedUnix
// and no started field, and models/issues/milestone.go the same. A bar therefore needs a
// start from somewhere else, and every bar states which source produced each of its
// endpoints (O7, O8). Presenting an estimated bar as a measured one is this view's
// characteristic failure, so an inferred end is flagged rather than merely rendered.
//
// Everything in this file is pure. The API handler reads the rows; this decides the shape.

// StartSource names where a bar's start came from (O8).
type StartSource string

const (
	// StartFromProgress is ccpm's own record: `started:` in updates/<N>/progress.md,
	// carried onto the issue by issue-sync as a `ccpm:started=` marker on the progress
	// comment. It is the only recorded start that exists.
	StartFromProgress StartSource = "ccpm_started"
	// StartFromCreated is the issue's creation time, the fallback O7 names.
	StartFromCreated StartSource = "issue_created"
	// StartNone is an issue ccpm does not manage: it has no start to draw (O10).
	StartNone StartSource = "none"
)

// EndSource names where a bar's end came from (O8).
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
	StartSources = []string{string(StartFromProgress), string(StartFromCreated), string(StartNone)}
	EndSources   = []string{string(EndFromClosed), string(EndFromDeadline), string(EndFromEstimate)}
)

// Inferred reports whether an end source is an estimate rather than a record (O8).
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

// startedMarker is the trailer issue-sync posts on a progress comment. It is the only
// carrier of ccpm's `started:` onto the forge, because Gitea has nowhere to store one.
var startedMarker = regexp.MustCompile(`ccpm:started=([0-9TZ:+\-]{4,40})`)

// effortLine reads the size token out of an Effort Estimate or Timebox line.
var effortLine = regexp.MustCompile(`(?i)^[-*\s]*(?:size|timebox|effort)\s*:\s*([A-Za-z0-9.]+)`)

// durationLiteral is a bare `3d` / `4h` / `90m` estimate.
var durationLiteral = regexp.MustCompile(`^(\d+(?:\.\d+)?)([hdwm])$`)

// StartedMarkerComment is the body of the comment a start-date write posts. It is the exact
// inverse of ParseStartedMarker, so the chart reads back what the chart wrote: a start lives
// on a comment because Gitea has nowhere else to keep one, and the progress file ccpm syncs
// from is not something the forge can read.
func StartedMarkerComment(startedUnix int64) string {
	return "ccpm:started=" + time.Unix(startedUnix, 0).UTC().Format(time.RFC3339)
}

// ParseStartedMarker reads a ccpm:started marker out of a comment body, returning the
// RFC 3339 text it carries. It returns "" when the comment carries none, which is the normal
// case: most comments are not progress updates.
func ParseStartedMarker(body string) string {
	m := startedMarker.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

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
	// unmanaged issue gets no bar and one stated reason (O10).
	Managed bool
	Epic    string
	// StartedUnix is the ccpm:started marker, 0 when the issue carries none.
	StartedUnix  int64
	CreatedUnix  int64
	ClosedUnix   int64
	DeadlineUnix int64
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
	Milestone   string `json:"milestone,omitempty"`
	MilestoneID int64  `json:"milestone_id,omitempty"`
	StartUnix   int64  `json:"start_unix"`
	EndUnix     int64  `json:"end_unix"`
	// StartSource and EndSource are on every bar, because O8 makes declaring them the
	// point of the view rather than a detail of it.
	StartSource StartSource `json:"start_source"`
	EndSource   EndSource   `json:"end_source"`
	// EndInferred is EndSource.Inferred(), published so a client renders the distinction
	// without knowing the enumeration.
	EndInferred bool `json:"end_inferred"`
	IsClosed    bool `json:"is_closed"`
}

// Unmanaged is an issue with no bar, listed beside the chart with the reason (O10).
type Unmanaged struct {
	IssueID int64  `json:"issue_id"`
	Number  int64  `json:"number"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Reason  string `json:"reason"`
	// SuggestedAction is what to do about it, like every other refusal (A21).
	SuggestedAction string `json:"suggested_action"`
}

// ResolveBar decides one bar's endpoints and names the source of each (O7, O8).
//
// It returns ok=false for an issue ccpm does not manage: that issue has no start to draw and
// is listed with its reason instead of being given a fabricated bar (O10).
func ResolveBar(in BarInput) (Bar, bool) {
	if !in.Managed {
		return Bar{}, false
	}

	bar := Bar{
		IssueID: in.IssueID, Number: in.Number, Title: in.Title, URL: in.URL,
		Epic: in.Epic, Milestone: in.Milestone, MilestoneID: in.MilestoneID,
		IsClosed: in.IsClosed,
	}

	switch {
	case in.StartedUnix > 0:
		bar.StartUnix, bar.StartSource = in.StartedUnix, StartFromProgress
	default:
		bar.StartUnix, bar.StartSource = in.CreatedUnix, StartFromCreated
	}

	// O7's order: the close time when closed, the deadline when set, otherwise the effort
	// estimate applied to the start.
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

// UnmanagedFor states why an issue has no bar (O10, A21).
func UnmanagedFor(in BarInput) Unmanaged {
	return Unmanaged{
		IssueID: in.IssueID, Number: in.Number, Title: in.Title, URL: in.URL,
		Reason: "ccpm does not manage this issue: it carries no " + EpicLabelPrefix + "<name> label, so there is no start to draw",
		SuggestedAction: "Import it into an epic, or add an " + EpicLabelPrefix + "<name> label to it. " +
			"A bar drawn from creation alone would present a guess as a schedule.",
	}
}

// ArrowKind distinguishes a hard gate from a sequencing hint (O9, N2). They do not read the
// same on a schedule: one is enforced by the forge, the other is advice.
type ArrowKind string

const (
	// ArrowGate is depends_on / blocked-by: written to issue_dependency, and Gitea itself
	// refuses to close the blocked issue (N1, N3).
	ArrowGate ArrowKind = "depends_on"
	// ArrowSequence is predecessor / successor: recorded as a rendered cross-reference and
	// enforced by nothing (N9, N10).
	ArrowSequence ArrowKind = "predecessor"
)

// ArrowKinds is the published set.
var ArrowKinds = []string{string(ArrowGate), string(ArrowSequence)}

// ArrowKindFor maps a relation word onto the arrow it draws. A word in the vocabulary that
// carries no ordering — related, duplicate-of, caused-by, parent — draws no arrow at all,
// because it says nothing about a schedule (N2).
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
}

// NewArrow builds an edge, filling in whether the forge enforces it.
func NewArrow(from, to int64, kind ArrowKind) Arrow {
	return Arrow{FromIssueID: from, ToIssueID: to, Kind: kind, Enforced: kind == ArrowGate}
}

// relationLine matches the cross-reference lines ccpm renders into an issue body under its
// Relations heading: "Predecessor #12", "Successor #13" (N4).
var relationLine = regexp.MustCompile(`(?i)^(predecessor|successor)\s+#(\d+)\s*$`)

// ParseSequenceRelations reads the sequencing edges out of an issue body. The enforced edges
// are deliberately NOT read from here: they live in issue_dependency, and reading them from
// the body too would make the timeline unable to say which source an edge came from (N8).
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

// SpanRow is an epic or milestone row: it spans the earliest start to the latest end of its
// children, and its progress is ccpm's existing task-close percentage (O11). No second
// definition of progress is introduced.
type SpanRow struct {
	Kind      string `json:"kind"` // "epic" or "milestone"
	Key       string `json:"key"`
	Label     string `json:"label"`
	StartUnix int64  `json:"start_unix"`
	EndUnix   int64  `json:"end_unix"`
	Children  int    `json:"children"`
	Closed    int    `json:"closed"`
	// Progress is closed children over all children, as a whole percentage.
	Progress int `json:"progress"`
	// EndInferred is true when ANY child's end is inferred, so a span is never drawn as
	// firmer than the bars it is made of (O8).
	EndInferred bool `json:"end_inferred"`
}

// BuildSpan folds a set of bars into one row. A span over no bars is not a row: it returns
// ok=false rather than a zero-width bar at the epoch.
func BuildSpan(kind, key, label string, bars []Bar) (SpanRow, bool) {
	if len(bars) == 0 {
		return SpanRow{}, false
	}
	row := SpanRow{Kind: kind, Key: key, Label: label, StartUnix: bars[0].StartUnix, EndUnix: bars[0].EndUnix}
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

// BuildSpans folds bars into the epic and milestone rows of O11, epics first and each set
// ordered by its own start.
func BuildSpans(bars []Bar) []SpanRow {
	byEpic := map[string][]Bar{}
	byMilestone := map[string][]Bar{}
	milestoneLabel := map[string]string{}
	for _, bar := range bars {
		if bar.Epic != "" {
			byEpic[bar.Epic] = append(byEpic[bar.Epic], bar)
		}
		if bar.MilestoneID > 0 {
			key := strconv.FormatInt(bar.MilestoneID, 10)
			byMilestone[key] = append(byMilestone[key], bar)
			milestoneLabel[key] = bar.Milestone
		}
	}

	rows := make([]SpanRow, 0, len(byEpic)+len(byMilestone))
	for _, key := range sortedKeys(byEpic) {
		if row, ok := BuildSpan("epic", key, key, byEpic[key]); ok {
			rows = append(rows, row)
		}
	}
	for _, key := range sortedKeys(byMilestone) {
		if row, ok := BuildSpan("milestone", key, milestoneLabel[key], byMilestone[key]); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

func sortedKeys(m map[string][]Bar) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
