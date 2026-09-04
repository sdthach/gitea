// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"testing"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"
	"gitea.dev/modules/timeutil"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlanningRoadmapPageOverFiftyBars measures, rather than assumes, that GetRoadmap's own
// page size follows the hub's own query grammar (limit up to 200) instead of being silently
// re-clamped to Gitea's smaller, unrelated API default of 50: 60 managed issues at limit=200
// must all draw their own bar.
func TestPlanningRoadmapPageOverFiftyBars(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	ty := issueType(t, 1, "bulk-managed", "#112233", "octicon-issue-opened", 9)

	const count = 60
	rows := make([]*issues_model.Issue, 0, count)
	assignments := make([]*planning_model.IssueTypeAssignment, 0, count)
	for i := range count {
		id := int64(9800 + i)
		rows = append(rows, &issues_model.Issue{
			ID: id, RepoID: 1, Index: id, PosterID: 2, Title: "bulk",
			CreatedUnix: timeutil.TimeStamp(1000 + int64(i)), UpdatedUnix: timeutil.TimeStamp(1000 + int64(i)),
		})
		assignments = append(assignments, &planning_model.IssueTypeAssignment{IssueID: id, TypeID: ty.ID})
	}
	require.NoError(t, db.Insert(t.Context(), rows))
	require.NoError(t, db.Insert(t.Context(), assignments))

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	payload := getRoadmap(t, token, "repo_id=1&limit=200")

	found := 0
	for _, row := range rows {
		for _, bar := range payload.Bars {
			if bar.IssueID == row.ID {
				found++
				break
			}
		}
	}
	assert.Equal(t, count, found, "every one of the 60 bulk-managed issues gets its own bar at limit=200")
	assert.False(t, payload.Truncated, "60 rows fit well inside the 200-row page")
}
