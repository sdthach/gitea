// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	created  = int64(1_700_000_000)
	started  = int64(1_700_100_000)
	closed   = int64(1_700_500_000)
	deadline = int64(1_700_900_000)
)

func managed(in BarInput) BarInput {
	in.Managed = true
	in.IssueID, in.Number, in.Title, in.Epic = 9001, 1, "task", "checkout"
	in.CreatedUnix = created
	return in
}

// Each of the three start sources, with the source label asserted.
func TestDeliveryResolveBarNamesItsStartSource(t *testing.T) {
	bar, ok := ResolveBar(managed(BarInput{StartedUnix: started, EffortSeconds: 2 * 86400}))
	require.True(t, ok)
	assert.Equal(t, StartFromProgress, bar.StartSource, "a recorded ccpm start wins")
	assert.Equal(t, started, bar.StartUnix)

	bar, ok = ResolveBar(managed(BarInput{EffortSeconds: 2 * 86400}))
	require.True(t, ok)
	assert.Equal(t, StartFromCreated, bar.StartSource, "with no record the start falls back to creation")
	assert.Equal(t, created, bar.StartUnix)

	// The third start source is the absence of one: an unmanaged issue has no bar at all.
	_, ok = ResolveBar(BarInput{IssueID: 9002, Number: 2, CreatedUnix: created})
	assert.False(t, ok)
	assert.Equal(t, StartNone, StartSource("none"))
	assert.Equal(t, []string{"ccpm_started", "issue_created", "none"}, StartSources)
}

// Each of the three end sources, with the source label asserted, and the inferred one
// distinguishable from the recorded ones.
func TestDeliveryResolveBarNamesItsEndSourceAndFlagsAnInferredOne(t *testing.T) {
	bar, ok := ResolveBar(managed(BarInput{StartedUnix: started, IsClosed: true, ClosedUnix: closed, DeadlineUnix: deadline}))
	require.True(t, ok)
	assert.Equal(t, EndFromClosed, bar.EndSource, "a close time outranks a deadline")
	assert.Equal(t, closed, bar.EndUnix)
	assert.False(t, bar.EndInferred, "a close time is a record")

	bar, ok = ResolveBar(managed(BarInput{StartedUnix: started, DeadlineUnix: deadline}))
	require.True(t, ok)
	assert.Equal(t, EndFromDeadline, bar.EndSource)
	assert.Equal(t, deadline, bar.EndUnix)
	assert.False(t, bar.EndInferred, "a deadline is a record")

	bar, ok = ResolveBar(managed(BarInput{EffortSeconds: 3 * 86400}))
	require.True(t, ok)
	assert.Equal(t, EndFromEstimate, bar.EndSource)
	assert.Equal(t, created+3*86400, bar.EndUnix, "the estimate is applied to the start")
	assert.True(t, bar.EndInferred, "presenting an estimate as a measurement is this view's characteristic failure")
	assert.Equal(t, []string{"closed", "deadline", "effort_estimate"}, EndSources)
}

// A task with a recorded start and a close time draws from actuals; one with neither
// draws from created plus estimate and is visually distinct.
func TestDeliveryResolveBarDrawsFromActualsOrFromEstimateAndSaysWhich(t *testing.T) {
	actual, ok := ResolveBar(managed(BarInput{StartedUnix: started, IsClosed: true, ClosedUnix: closed}))
	require.True(t, ok)
	assert.Equal(t, StartFromProgress, actual.StartSource)
	assert.Equal(t, EndFromClosed, actual.EndSource)
	assert.False(t, actual.EndInferred)

	guess, ok := ResolveBar(managed(BarInput{}))
	require.True(t, ok)
	assert.Equal(t, StartFromCreated, guess.StartSource)
	assert.Equal(t, EndFromEstimate, guess.EndSource)
	assert.True(t, guess.EndInferred)
	assert.Equal(t, created+DefaultEffortDays*86400, guess.EndUnix, "an unstated estimate still gives the bar a width")
}

func TestDeliveryResolveBarClampsAnEndThatPrecedesItsStart(t *testing.T) {
	bar, ok := ResolveBar(managed(BarInput{StartedUnix: started, DeadlineUnix: created - 86400}))
	require.True(t, ok)
	assert.Equal(t, EndFromDeadline, bar.EndSource, "a deadline before the start is real data and keeps its source")
	assert.Equal(t, bar.StartUnix, bar.EndUnix, "the bar is clamped rather than drawn backwards")
}

// An issue ccpm does not manage is listed with the reason, never given a bar.
func TestDeliveryUnmanagedIssueGetsNoBarAndOneStatedReason(t *testing.T) {
	in := BarInput{IssueID: 9002, Number: 7, Title: "filed by hand", URL: "/acme/widgets/issues/7", CreatedUnix: created}

	_, ok := ResolveBar(in)
	assert.False(t, ok, "no bar is fabricated from creation alone")

	listed := UnmanagedFor(in)
	assert.Equal(t, int64(7), listed.Number)
	assert.Contains(t, listed.Reason, "ccpm does not manage this issue")
	assert.Contains(t, listed.Reason, EpicLabelPrefix)
	assert.NotEmpty(t, listed.SuggestedAction, "every error carries a suggested next action")
}

// A hard gate and a sequencing hint do not read the same on a schedule.
func TestDeliveryArrowKindPerRelationType(t *testing.T) {
	for _, word := range []string{"depends_on", "blocked-by", "blocked_by", "blocks"} {
		kind, ok := ArrowKindFor(word)
		require.True(t, ok, word)
		assert.Equal(t, ArrowGate, kind)
		assert.True(t, NewArrow(1, 2, kind).Enforced, "the forge itself refuses the close")
	}
	for _, word := range []string{"predecessor", "successor", "PREDECESSOR"} {
		kind, ok := ArrowKindFor(word)
		require.True(t, ok, word)
		assert.Equal(t, ArrowSequence, kind)
		assert.False(t, NewArrow(1, 2, kind).Enforced, "sequencing is enforced by nothing")
	}
	// Vocabulary words that say nothing about a schedule draw no arrow at all.
	for _, word := range []string{"related", "duplicate-of", "caused-by", "causes", "parent", "child", ""} {
		_, ok := ArrowKindFor(word)
		assert.False(t, ok, "%q carries no ordering, so it draws no arrow", word)
	}
}

func TestDeliveryParseStartedMarkerReadsCcpmsOwnRecord(t *testing.T) {
	body := "## Progress Update\n\n---\n*Progress: 40%*\n\n<!-- ccpm:started=2026-08-31T22:33:25Z -->\n"
	assert.Equal(t, "2026-08-31T22:33:25Z", ParseStartedMarker(body))
	assert.Empty(t, ParseStartedMarker("a plain comment with no marker"),
		"most comments are not progress updates, and that is not an error")
}

func TestDeliveryParseEffortSecondsReadsTheRenderedSection(t *testing.T) {
	assert.Equal(t, 5*int64(86400), ParseEffortSeconds("### Effort Estimate\n\n- Size: M\n"))
	assert.Equal(t, 1*int64(86400), ParseEffortSeconds("### Effort Estimate\n\n- Size: XS\n"))
	assert.Equal(t, 20*int64(86400), ParseEffortSeconds("- size: xl"))
	assert.Equal(t, int64(4*3600), ParseEffortSeconds("### Timebox\n\nTimebox: 4h\n"), "a spike states a timebox, not a size")
	assert.Equal(t, 3*int64(86400), ParseEffortSeconds("Size: 3d"))
	assert.Equal(t, DefaultEffortDays*86400, ParseEffortSeconds("### Description\n\nno estimate here\n"),
		"a bar has to have a width; the source label is what says it was inferred")
	assert.Equal(t, DefaultEffortDays*86400, ParseEffortSeconds("- Size: enormous"))
}

// The sequencing edges come from the rendered body, the enforced ones from
// issue_dependency, so the timeline can always say which source an edge came from.
func TestDeliveryParseSequenceRelationsReadsOnlyTheUnenforcedWords(t *testing.T) {
	body := "### Relations\n\nPredecessor #12\nSuccessor #13\nBlocked by #14\nRelated to #15\nCaused by #16\n"
	assert.Equal(t, [][2]string{{"predecessor", "12"}, {"successor", "13"}}, ParseSequenceRelations(body))
	assert.Empty(t, ParseSequenceRelations("### Description\n\nnothing here\n"))
}

// An epic or milestone row spans earliest start to latest end of its children, and its
// progress is ccpm's existing task-close percentage.
func TestDeliveryBuildSpanCoversItsChildrenAndUsesCcpmsProgress(t *testing.T) {
	bars := []Bar{
		{IssueID: 1, StartUnix: 300, EndUnix: 900, IsClosed: true},
		{IssueID: 2, StartUnix: 100, EndUnix: 400},
		{IssueID: 3, StartUnix: 500, EndUnix: 1200, EndInferred: true},
	}
	row, ok := BuildSpan("epic", "checkout", "checkout", bars)
	require.True(t, ok)
	assert.Equal(t, int64(100), row.StartUnix)
	assert.Equal(t, int64(1200), row.EndUnix)
	assert.Equal(t, 3, row.Children)
	assert.Equal(t, 1, row.Closed)
	assert.Equal(t, 33, row.Progress, "closed over total, ccpm's own definition and no second one")
	assert.True(t, row.EndInferred, "a span is never firmer than the bars it is made of")

	_, ok = BuildSpan("epic", "empty", "empty", nil)
	assert.False(t, ok, "a span over no bars is not a row")
}

func TestDeliveryBuildSpansEmitsEpicsThenMilestones(t *testing.T) {
	bars := []Bar{
		{IssueID: 1, Epic: "checkout", StartUnix: 100, EndUnix: 200, MilestoneID: 4, Milestone: "beta"},
		{IssueID: 2, Epic: "billing", StartUnix: 150, EndUnix: 900, IsClosed: true},
		{IssueID: 3, StartUnix: 50, EndUnix: 60},
	}
	rows := BuildSpans(bars)
	require.Len(t, rows, 3)
	assert.Equal(t, []string{"epic", "epic", "milestone"}, []string{rows[0].Kind, rows[1].Kind, rows[2].Kind})
	assert.Equal(t, "billing", rows[0].Key)
	assert.Equal(t, "checkout", rows[1].Key)
	assert.Equal(t, "beta", rows[2].Label)
	assert.Equal(t, 100, rows[0].Progress)
	assert.Equal(t, 0, rows[1].Progress)
}

// TestDeliveryBuildSpansExcludesTheEpicIssueFromItsOwnRollup is what makes the containment
// check meaningful: ccpm puts epic:<name> on the epic's own issue beside type:epic, so
// without the exclusion the parent counts among its own children and its declared window has
// nothing left to be compared against.
func TestDeliveryBuildSpansExcludesTheEpicIssueFromItsOwnRollup(t *testing.T) {
	bars := []Bar{
		{IssueID: 42, Number: 42, Epic: "checkout", Type: TypeEpic, StartUnix: 100, EndUnix: 5000},
		{IssueID: 1, Number: 57, Epic: "checkout", Type: "story", StartUnix: 300, EndUnix: 900, IsClosed: true},
		{IssueID: 2, Number: 58, Epic: "checkout", Type: "task", StartUnix: 200, EndUnix: 400},
	}
	rows := BuildSpans(bars)
	require.Len(t, rows, 1)
	assert.Equal(t, 2, rows[0].Children, "the epic issue is not one of its own children")
	assert.Equal(t, 50, rows[0].Progress, "progress is over the children, not over the parent too")
	assert.Equal(t, int64(200), rows[0].StartUnix, "the derived window is the children's")
	assert.Equal(t, int64(900), rows[0].EndUnix)
	assert.EqualValues(t, 42, rows[0].IssueID, "the row names the epic issue, so a bracket can be opened")

	// The same epic issue filed under a milestone is genuinely one of that milestone's
	// issues, so milestone rollups are unchanged.
	for i := range bars {
		bars[i].MilestoneID, bars[i].Milestone = 4, "beta"
	}
	rows = BuildSpans(bars)
	require.Len(t, rows, 2)
	assert.Equal(t, 3, rows[1].Children, "a milestone counts every issue filed under it")
}

// TestDeliveryContainmentFlagsAnEpicThatEndsBeforeItsChildren is the warning the chart is the
// only place to see, and the case it must NOT fire on: children running in parallel past no
// deadline at all.
func TestDeliveryContainmentFlagsAnEpicThatEndsBeforeItsChildren(t *testing.T) {
	// 2026-03-11 declared, 2026-03-25 derived: a fortnight of overhang.
	declared := Bar{IssueID: 42, Number: 42, Epic: "checkout", Type: TypeEpic, StartUnix: 1772323200, EndUnix: 1773187200}
	child := Bar{IssueID: 7, Number: 57, Epic: "checkout", Type: "story", StartUnix: 1772323200, EndUnix: 1774396800}

	rows := BuildSpans([]Bar{declared, child})
	require.Len(t, rows, 1)
	row := rows[0]
	assert.False(t, row.ContainsChildren)
	assert.EqualValues(t, 1773187200, row.DeclaredEndUnix)
	assert.EqualValues(t, 1774396800, row.EndUnix)
	assert.Equal(t, "epic checkout (#42) ends 14 days before the work filed under it", row.Warning)
	assert.Equal(t, "Move the epic's deadline to 2026-03-25, or move story #57 earlier.", row.SuggestedAction)

	// A declared window that contains its children is not flagged, however many of them run
	// in parallel: the check is containment, not a sum of effort.
	declared.EndUnix = 1774396800
	second := child
	second.IssueID, second.Number = 8, 58
	rows = BuildSpans([]Bar{declared, child, second})
	require.Len(t, rows, 1)
	assert.True(t, rows[0].ContainsChildren)
	assert.Empty(t, rows[0].Warning)
	assert.Empty(t, rows[0].SuggestedAction)

	// Work that starts before the epic it belongs to is the other half of containment.
	declared.StartUnix = 1772928000 // 2026-03-08
	rows = BuildSpans([]Bar{declared, child})
	require.Len(t, rows, 1)
	assert.False(t, rows[0].ContainsChildren)
	assert.Equal(t, "epic checkout (#42) starts 7 days after the work filed under it", rows[0].Warning)
	assert.Contains(t, rows[0].SuggestedAction, "Move the epic's start to 2026-03-01")

	// An epic label naming no epic issue has no declared window to contradict.
	rows = BuildSpans([]Bar{child})
	require.Len(t, rows, 1)
	assert.True(t, rows[0].ContainsChildren)
	assert.Zero(t, rows[0].IssueID)
}

// TestDeliverySpanRowPartialWithdrawsItsProgress: a fraction of an unknown denominator is not
// a measurement, so a capped rollup publishes no percentage.
func TestDeliverySpanRowPartialWithdrawsItsProgress(t *testing.T) {
	row, ok := BuildSpan("epic", "checkout", "checkout", []Bar{
		{IssueID: 1, StartUnix: 100, EndUnix: 200, IsClosed: true},
		{IssueID: 2, StartUnix: 100, EndUnix: 200},
	})
	require.True(t, ok)
	require.Equal(t, 50, row.Progress)

	row.MarkPartial()
	assert.True(t, row.Partial)
	assert.Zero(t, row.Progress)
}

// TestDeliveryResolveBarReadsItsTypeOffTheLabels: the chart cannot tell a story from a bug
// from an epic without it, and the epic self-exclusion depends on the answer.
func TestDeliveryResolveBarReadsItsTypeOffTheLabels(t *testing.T) {
	bar, ok := ResolveBar(managed(BarInput{
		Labels: []string{"epic:checkout", "type:story"}, Assignees: []string{"jo"},
	}))
	require.True(t, ok)
	assert.Equal(t, "story", bar.Type)
	assert.Equal(t, []string{"epic:checkout", "type:story"}, bar.Labels)
	assert.Equal(t, []string{"jo"}, bar.Assignees)
	assert.Equal(t, "story", LaneKeyFor(bar.Labels, bar.Assignees, GroupType),
		"the chart's lanes and the board's are one definition")

	bar, ok = ResolveBar(managed(BarInput{Labels: []string{"epic:checkout"}}))
	require.True(t, ok)
	assert.Empty(t, bar.Type, "an issue with no type: label has no type, rather than a guessed one")
}

// TestDeliveryParseZoomAcceptsTheDeclaredSetAndRefusesTheRest mirrors the grouping parser: an
// unknown value is refused rather than silently becoming the default.
func TestDeliveryParseZoomAcceptsTheDeclaredSetAndRefusesTheRest(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want Zoom
	}{
		{"", ZoomIssue}, {"issue", ZoomIssue}, {"epic", ZoomEpic}, {" MILESTONE ", ZoomMilestone},
	} {
		zoom, ok := ParseZoom(tc.raw)
		require.True(t, ok, tc.raw)
		assert.Equal(t, tc.want, zoom, tc.raw)
	}
	zoom, ok := ParseZoom("initiative")
	assert.False(t, ok, "a level that does not ship yet is refused, not silently downgraded")
	assert.Equal(t, ZoomIssue, zoom)
}

// TestDeliveryBuildSpansListsAnEpicThatHasNoChildrenYet: a freshly filed epic has a window
// and no children, and drawing nothing for it would say nothing about it.
func TestDeliveryBuildSpansListsAnEpicThatHasNoChildrenYet(t *testing.T) {
	declared := Bar{IssueID: 42, Number: 42, Epic: "lonely", Type: TypeEpic, StartUnix: 100, EndUnix: 900, EndInferred: true}

	rows := BuildSpans([]Bar{declared})
	require.Len(t, rows, 1)
	assert.Equal(t, "epic", rows[0].Kind)
	assert.Equal(t, "lonely", rows[0].Key)
	assert.Zero(t, rows[0].Children)
	assert.Zero(t, rows[0].Closed)
	assert.Zero(t, rows[0].Progress)
	assert.Equal(t, int64(100), rows[0].StartUnix, "the row spans the epic's own declared window")
	assert.Equal(t, int64(900), rows[0].EndUnix)
	assert.EqualValues(t, 42, rows[0].IssueID)
	assert.EqualValues(t, 100, rows[0].DeclaredStartUnix)
	assert.EqualValues(t, 900, rows[0].DeclaredEndUnix)
	assert.True(t, rows[0].ContainsChildren, "a set of nothing is contained by any window")
	assert.Empty(t, rows[0].Warning, "an epic with nothing filed under it contradicts nothing")
	assert.True(t, rows[0].EndInferred, "the declared bar's own end is an estimate")

	// An epic label carried by no epic issue and no child is still not a row.
	assert.Empty(t, BuildSpans([]Bar{{IssueID: 7, Epic: "", Type: "story", StartUnix: 1, EndUnix: 2}}))
}
