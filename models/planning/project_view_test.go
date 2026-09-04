// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"testing"

	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanningProjectViewListInsertDelete(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	require.NoError(t, InsertProjectView(ctx, &ProjectView{ProjectID: 1, Name: "open bugs", Query: "state:open", CreatedBy: 2}))
	require.NoError(t, InsertProjectView(ctx, &ProjectView{ProjectID: 1, Name: "closed", Query: "state:closed", CreatedBy: 2}))
	require.NoError(t, InsertProjectView(ctx, &ProjectView{ProjectID: 2, Name: "open bugs", Query: "state:open", CreatedBy: 3}))

	rows, err := ListProjectViews(ctx, 1)
	require.NoError(t, err)
	require.Len(t, rows, 2, "only project 1's own views")
	assert.Equal(t, "closed", rows[0].Name, "alphabetical order")

	exists, err := ProjectViewNameExists(ctx, 1, "open bugs")
	require.NoError(t, err)
	assert.True(t, exists)
	exists, err = ProjectViewNameExists(ctx, 2, "closed")
	require.NoError(t, err)
	assert.False(t, exists, "a name is unique per project, not globally")

	require.NoError(t, DeleteProjectView(ctx, rows[1].ID))
	rows, err = ListProjectViews(ctx, 1)
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

func TestPlanningProjectViewNameExistsCaseInsensitive(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	require.NoError(t, InsertProjectView(ctx, &ProjectView{ProjectID: 1, Name: "dup", Query: "state:open", CreatedBy: 2}))

	exists, err := ProjectViewNameExists(ctx, 1, "DUP")
	require.NoError(t, err)
	assert.True(t, exists, "name comparison is case-insensitive")
}

func TestPlanningGetProjectViewNotFound(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	_, err := GetProjectView(t.Context(), 999999)
	require.Error(t, err)
}
