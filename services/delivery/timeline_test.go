// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

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

// SC 39: each of the three start sources, with the source label asserted.
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

// SC 39: each of the three end sources, with the source label asserted, and the inferred one
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

// SC 39: a task with a recorded start and a close time draws from actuals; one with neither
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

// SC 39, O10: an issue ccpm does not manage is listed with the reason, never given a bar.
func TestDeliveryUnmanagedIssueGetsNoBarAndOneStatedReason(t *testing.T) {
	in := BarInput{IssueID: 9002, Number: 7, Title: "filed by hand", URL: "/acme/widgets/issues/7", CreatedUnix: created}

	_, ok := ResolveBar(in)
	assert.False(t, ok, "no bar is fabricated from creation alone")

	listed := UnmanagedFor(in)
	assert.Equal(t, int64(7), listed.Number)
	assert.Contains(t, listed.Reason, "ccpm does not manage this issue")
	assert.Contains(t, listed.Reason, EpicLabelPrefix)
	assert.NotEmpty(t, listed.SuggestedAction, "every error carries a suggested next action (A21)")
}

// O9: a hard gate and a sequencing hint do not read the same on a schedule.
func TestDeliveryArrowKindPerRelationType(t *testing.T) {
	for _, word := range []string{"depends_on", "blocked-by", "blocked_by", "blocks"} {
		kind, ok := ArrowKindFor(word)
		require.True(t, ok, word)
		assert.Equal(t, ArrowGate, kind)
		assert.True(t, NewArrow(1, 2, kind).Enforced, "the forge itself refuses the close (N1, N3)")
	}
	for _, word := range []string{"predecessor", "successor", "PREDECESSOR"} {
		kind, ok := ArrowKindFor(word)
		require.True(t, ok, word)
		assert.Equal(t, ArrowSequence, kind)
		assert.False(t, NewArrow(1, 2, kind).Enforced, "sequencing is enforced by nothing (N9, N10)")
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

// N8: the sequencing edges come from the rendered body, the enforced ones from
// issue_dependency, so the timeline can always say which source an edge came from.
func TestDeliveryParseSequenceRelationsReadsOnlyTheUnenforcedWords(t *testing.T) {
	body := "### Relations\n\nPredecessor #12\nSuccessor #13\nBlocked by #14\nRelated to #15\nCaused by #16\n"
	assert.Equal(t, [][2]string{{"predecessor", "12"}, {"successor", "13"}}, ParseSequenceRelations(body))
	assert.Empty(t, ParseSequenceRelations("### Description\n\nnothing here\n"))
}

// O11: an epic or milestone row spans earliest start to latest end of its children, and its
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
