// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"testing"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanningUpsertUserCapacityInsertsThenReplaces(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	require.NoError(t, UpsertUserCapacity(ctx, &UserCapacity{UserID: 1, RepoID: 7, HoursPerDay: 6, Utilization: 0.5, Workdays: 62}))
	row, err := GetUserCapacity(ctx, 1, 7, 0)
	require.NoError(t, err)
	assert.InEpsilon(t, 6, row.HoursPerDay, 0.001)

	require.NoError(t, UpsertUserCapacity(ctx, &UserCapacity{UserID: 1, RepoID: 7, HoursPerDay: 4, Utilization: 0.9, Workdays: 127}))
	row, err = GetUserCapacity(ctx, 1, 7, 0)
	require.NoError(t, err)
	assert.InEpsilon(t, 4, row.HoursPerDay, 0.001)
	assert.InEpsilon(t, 0.9, row.Utilization, 0.001)
	assert.Equal(t, 127, row.Workdays)
}

func TestPlanningDeleteUserCapacityRemovesOnlyItsOwnScope(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	require.NoError(t, UpsertUserCapacity(ctx, &UserCapacity{UserID: 1, RepoID: 7, HoursPerDay: 6, Utilization: 0.5, Workdays: 62}))
	require.NoError(t, UpsertUserCapacity(ctx, &UserCapacity{UserID: 1, OrgID: 3, HoursPerDay: 8, Utilization: 0.8, Workdays: 62}))

	require.NoError(t, DeleteUserCapacity(ctx, 1, 7, 0))
	_, err := GetUserCapacity(ctx, 1, 7, 0)
	require.Error(t, err)
	_, err = GetUserCapacity(ctx, 1, 0, 3)
	require.NoError(t, err, "the org-scoped row is untouched")
}

func TestPlanningCapacitiesForReturnsRepoOrgAndInstanceInOneQuery(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	require.NoError(t, UpsertUserCapacity(ctx, &UserCapacity{UserID: 1, HoursPerDay: 8, Utilization: 0.8, Workdays: 62}))            // instance
	require.NoError(t, UpsertUserCapacity(ctx, &UserCapacity{UserID: 1, OrgID: 5, HoursPerDay: 7, Utilization: 0.7, Workdays: 62}))  // org
	require.NoError(t, UpsertUserCapacity(ctx, &UserCapacity{UserID: 1, RepoID: 7, HoursPerDay: 6, Utilization: 0.6, Workdays: 62})) // repo
	require.NoError(t, UpsertUserCapacity(ctx, &UserCapacity{UserID: 2, RepoID: 9, HoursPerDay: 5, Utilization: 0.5, Workdays: 62})) // a different repo, excluded

	rows, err := CapacitiesFor(ctx, []int64{1, 2}, 7, 5)
	require.NoError(t, err)
	assert.Len(t, rows, 3)
}

func TestPlanningTrackedByIssueUserGroupsAndExcludesDeleted(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	require.NoError(t, db.Insert(ctx, &issues_model.TrackedTime{IssueID: 1, UserID: 10, Time: 3600}))
	require.NoError(t, db.Insert(ctx, &issues_model.TrackedTime{IssueID: 1, UserID: 10, Time: 1800}))
	require.NoError(t, db.Insert(ctx, &issues_model.TrackedTime{IssueID: 1, UserID: 11, Time: 900}))
	require.NoError(t, db.Insert(ctx, &issues_model.TrackedTime{IssueID: 1, UserID: 10, Time: 999999, Deleted: true}))

	out, err := TrackedByIssueUser(ctx, []int64{1})
	require.NoError(t, err)
	assert.EqualValues(t, 5400, out[[2]int64{1, 10}])
	assert.EqualValues(t, 900, out[[2]int64{1, 11}])
}
