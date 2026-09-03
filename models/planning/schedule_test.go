// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"testing"

	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertIssueStartKeepsOneRowOnTheSecondValue(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	require.NoError(t, UpsertIssueStart(ctx, 1, 100))
	require.NoError(t, UpsertIssueStart(ctx, 1, 200))

	starts, err := IssueStarts(ctx, []int64{1})
	require.NoError(t, err)
	assert.Equal(t, map[int64]int64{1: 200}, starts, "the second upsert replaces the first rather than adding a row")

	require.NoError(t, DeleteIssueStart(ctx, 1))
	starts, err = IssueStarts(ctx, []int64{1})
	require.NoError(t, err)
	assert.Empty(t, starts, "a deleted start leaves no entry")
}

func TestUpsertMilestoneStartKeepsOneRowOnTheSecondValue(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	require.NoError(t, UpsertMilestoneStart(ctx, 1, 100))
	require.NoError(t, UpsertMilestoneStart(ctx, 1, 200))

	starts, err := MilestoneStarts(ctx, []int64{1})
	require.NoError(t, err)
	assert.Equal(t, map[int64]int64{1: 200}, starts)

	require.NoError(t, DeleteMilestoneStart(ctx, 1))
	starts, err = MilestoneStarts(ctx, []int64{1})
	require.NoError(t, err)
	assert.Empty(t, starts)
}

func TestParseStartedMarkerReadsTheLastValidValue(t *testing.T) {
	at, ok := ParseStartedMarker("## Progress\n\n<!-- ccpm:started=2026-08-31T22:33:25Z -->")
	require.True(t, ok)
	assert.EqualValues(t, 1788215605, at)

	_, ok = ParseStartedMarker("a plain comment with no marker")
	assert.False(t, ok)

	_, ok = ParseStartedMarker("ccpm:started=2026-13-99T99:99:99Z")
	assert.False(t, ok, "a malformed value is not a valid start")
}
