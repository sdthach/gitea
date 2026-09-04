// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"testing"

	"gitea.dev/models/db"
	planning_model "gitea.dev/models/planning"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrateFieldsSeedsPointsOnceAndLeavesAnExistingOneAlone covers both halves: the instance
// scope ends up with exactly one points field, int, labelled "Points", and a second call
// inserts nothing more — whether this is the first call in the whole test binary or a rerun
// after TestMigrateCopiesRowsWhenSyncCreatedTheNewTablesFirst already seeded it through the
// full Migrate chain.
//
// unittest.PrepareTestDatabase is deliberately not called here, matching every other migration
// test in this file: the package shares one database across its tests.
func TestMigrateFieldsSeedsPointsOnceAndLeavesAnExistingOneAlone(t *testing.T) {
	ctx := t.Context()
	e := db.GetEngine(ctx)

	require.NoError(t, migrateFields(ctx, e))

	rows, err := planning_model.FieldsInScopes(ctx, 0, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1, "exactly one instance-scoped field: seeded once, however many times this runs")
	assert.Equal(t, "points", rows[0].Key)
	assert.Equal(t, "Points", rows[0].Label)
	assert.Equal(t, "int", rows[0].Kind)

	countBefore, err := e.Count(new(planning_model.Field))
	require.NoError(t, err)

	require.NoError(t, migrateFields(ctx, e))

	countAfter, err := e.Count(new(planning_model.Field))
	require.NoError(t, err)
	assert.Equal(t, countBefore, countAfter, "a rerun inserts nothing")
}
