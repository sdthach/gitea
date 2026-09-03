// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"testing"

	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertType(t *testing.T, repoID, orgID int64, name string) *IssueType {
	t.Helper()
	row := &IssueType{RepoID: repoID, OrgID: orgID, Name: name, Color: "#ededed", Icon: "octicon-issue-opened", Rank: 3}
	require.NoError(t, InsertIssueType(t.Context(), row))
	return row
}

func TestPlanningInsertIssueTypeLowerCasesTheName(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	row := insertType(t, 1, 0, "Bug")
	assert.Equal(t, "bug", row.Name)

	row.Name = "Widget"
	require.NoError(t, UpdateIssueType(t.Context(), row))
	fetched, err := GetIssueType(t.Context(), row.ID)
	require.NoError(t, err)
	assert.Equal(t, "widget", fetched.Name, "an update lower-cases the name too")
}

func TestPlanningTypesInScopesReturnsInstanceOrgAndRepoInOneQuery(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	insertType(t, 0, 0, "epic")
	insertType(t, 0, 5, "story")
	insertType(t, 7, 0, "bug")
	insertType(t, 9, 0, "spike") // a different repo, must not appear

	rows, err := TypesInScopes(ctx, 7, 5)
	require.NoError(t, err)
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	assert.ElementsMatch(t, []string{"epic", "story", "bug"}, names)
}

func TestPlanningTypeExistsExcludesTheGivenID(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	row := insertType(t, 1, 0, "bug")

	has, err := TypeExists(ctx, 1, 0, "bug", 0)
	require.NoError(t, err)
	assert.True(t, has)

	has, err = TypeExists(ctx, 1, 0, "bug", row.ID)
	require.NoError(t, err)
	assert.False(t, has, "excluding the row's own id means updating it without changing the name is not a collision")
}

func TestPlanningUpsertAssignmentKeepsOneRowOnTheSecondValue(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	require.NoError(t, UpsertAssignment(ctx, 1, 100))
	require.NoError(t, UpsertAssignment(ctx, 1, 200))

	assigned, err := AssignmentsFor(ctx, []int64{1})
	require.NoError(t, err)
	assert.Equal(t, map[int64]int64{1: 200}, assigned, "the second upsert replaces the first rather than adding a row")

	count, err := CountAssignments(ctx, 200)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)

	ids, err := IssueIDsForType(ctx, 200)
	require.NoError(t, err)
	assert.Equal(t, []int64{1}, ids)

	require.NoError(t, DeleteAssignment(ctx, 1))
	assigned, err = AssignmentsFor(ctx, []int64{1})
	require.NoError(t, err)
	assert.Empty(t, assigned)

	count, err = CountAssignments(ctx, 200)
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestPlanningGetIssueTypeNotFound(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	_, err := GetIssueType(t.Context(), 987654)
	require.Error(t, err)
}
