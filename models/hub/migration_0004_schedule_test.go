// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"testing"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"
	"gitea.dev/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unittest.PrepareTestDatabase is deliberately not called here: TestMigrateRenamesTheOldTables
// in this same package leaves deploy_environment on its old, narrower schema for the rest of
// the binary, and reloading fixtures would trip over that — see its own comment. The comments
// this test needs are inserted directly instead.
//
// TestMigrateScheduleFromMarkersKeepsTheLastValidValue covers the cases the migration must
// get right: two markers on one issue leave the later one, a malformed marker beside a good
// one is skipped rather than clobbering it, a same-type comment with no marker is left out of
// the result without dropping a real marker on the same issue, and re-running inserts nothing
// because the row is already there.
func TestMigrateScheduleFromMarkersKeepsTheLastValidValue(t *testing.T) {
	ctx := t.Context()
	e := db.GetEngine(ctx)

	require.NoError(t, db.Insert(ctx, &issues_model.Comment{
		Type: issues_model.CommentTypeComment, IssueID: 1, PosterID: 2,
		Content: "## Progress\n\n<!-- ccpm:started=2026-01-01T00:00:00Z -->", CreatedUnix: 1000,
	}))
	require.NoError(t, db.Insert(ctx, &issues_model.Comment{
		Type: issues_model.CommentTypeComment, IssueID: 1, PosterID: 2,
		Content: "## Progress\n\n<!-- ccpm:started=2026-03-01T00:00:00Z -->", CreatedUnix: 2000,
	}))
	// A malformed marker posted after the good one must not erase it.
	require.NoError(t, db.Insert(ctx, &issues_model.Comment{
		Type: issues_model.CommentTypeComment, IssueID: 1, PosterID: 2,
		Content: "ccpm:started=2026-99-99T00:00:00Z", CreatedUnix: 3000,
	}))
	require.NoError(t, db.Insert(ctx, &issues_model.Comment{
		Type: issues_model.CommentTypeComment, IssueID: 5, PosterID: 2,
		Content: "ccpm:started=2026-06-01T00:00:00Z", CreatedUnix: 1000,
	}))
	// Same type, no marker: the SQL filter must not drop it from consideration in a way that
	// skips issue 5's own marker comment, and it must not itself produce a start.
	require.NoError(t, db.Insert(ctx, &issues_model.Comment{
		Type: issues_model.CommentTypeComment, IssueID: 5, PosterID: 2,
		Content: "just a regular comment, no marker here", CreatedUnix: 1500,
	}))

	require.NoError(t, migrateScheduleFromMarkers(ctx, e))

	starts, err := planning_model.IssueStarts(ctx, []int64{1, 5})
	require.NoError(t, err)
	assert.EqualValues(t, 1772323200, starts[1], "2026-03-01T00:00:00Z: the last valid marker wins")
	assert.EqualValues(t, 1780272000, starts[5], "2026-06-01T00:00:00Z")

	// A rerun must not touch an issue that already has a row: it changes the row to a value
	// a real migration would never write, and asserts it survives.
	_, err = e.Where("issue_id = ?", int64(1)).Cols("start_unix").
		Update(&planning_model.IssueSchedule{StartUnix: timeutil.TimeStamp(999)})
	require.NoError(t, err)
	require.NoError(t, migrateScheduleFromMarkers(ctx, e))
	starts, err = planning_model.IssueStarts(ctx, []int64{1})
	require.NoError(t, err)
	assert.EqualValues(t, 999, starts[1], "an issue that already has a schedule row is left alone")
}
