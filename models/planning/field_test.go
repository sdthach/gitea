// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"testing"

	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertField(t *testing.T, repoID, orgID int64, key, kind string) *Field {
	t.Helper()
	row := &Field{RepoID: repoID, OrgID: orgID, Key: key, Label: key, Kind: kind}
	require.NoError(t, InsertField(t.Context(), row))
	return row
}

func TestPlanningFieldsInScopesReturnsInstanceOrgAndRepoInOneQuery(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	insertField(t, 0, 0, "points", "int")
	insertField(t, 0, 5, "severity", "select")
	insertField(t, 7, 0, "due", "date")
	insertField(t, 9, 0, "notes", "text") // a different repo, must not appear

	rows, err := FieldsInScopes(ctx, 7, 5)
	require.NoError(t, err)
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.Key)
	}
	assert.ElementsMatch(t, []string{"points", "severity", "due"}, keys)
}

func TestPlanningFieldExistsExcludesTheGivenID(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	row := insertField(t, 1, 0, "points", "int")

	has, err := FieldExists(ctx, 1, 0, "points", 0)
	require.NoError(t, err)
	assert.True(t, has)

	has, err = FieldExists(ctx, 1, 0, "points", row.ID)
	require.NoError(t, err)
	assert.False(t, has, "excluding the row's own id lets an update keep its own key")
}

func TestPlanningFieldRoundTripsOptions(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	row := &Field{RepoID: 1, Key: "severity", Label: "Severity", Kind: "select", Options: []string{"low", "high"}}
	require.NoError(t, InsertField(ctx, row))

	fetched, err := GetField(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"low", "high"}, fetched.Options)

	fetched.Options = []string{"low", "medium", "high"}
	require.NoError(t, UpdateField(ctx, fetched))
	reread, err := GetField(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"low", "medium", "high"}, reread.Options)
}

func TestPlanningDeleteFieldCascadesItsValuesAndReturnsTheCount(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	row := insertField(t, 1, 0, "points", "int")
	require.NoError(t, UpsertValue(ctx, &FieldValue{IssueID: 1, FieldID: row.ID, ValueInt: 3}))
	require.NoError(t, UpsertValue(ctx, &FieldValue{IssueID: 2, FieldID: row.ID, ValueInt: 5}))

	count, err := DeleteField(ctx, row.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)

	_, err = GetField(ctx, row.ID)
	require.Error(t, err)
	values, err := ValuesFor(ctx, []int64{1, 2})
	require.NoError(t, err)
	assert.Empty(t, values)
}

func TestPlanningUpsertValueReplacesRatherThanDuplicates(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	row := insertField(t, 1, 0, "points", "int")

	require.NoError(t, UpsertValue(ctx, &FieldValue{IssueID: 1, FieldID: row.ID, ValueInt: 3}))
	require.NoError(t, UpsertValue(ctx, &FieldValue{IssueID: 1, FieldID: row.ID, ValueInt: 8}))

	values, err := ValuesFor(ctx, []int64{1})
	require.NoError(t, err)
	require.Len(t, values[1], 1)
	assert.EqualValues(t, 8, values[1][row.ID].ValueInt)

	require.NoError(t, DeleteValue(ctx, 1, row.ID))
	values, err = ValuesFor(ctx, []int64{1})
	require.NoError(t, err)
	assert.Empty(t, values[1])
}
