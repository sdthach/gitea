// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"testing"
	"time"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unittest.PrepareTestDatabase is deliberately not called here: TestMigrateRenamesTheOldTables
// in this same package leaves deploy_environment on its old, narrower schema for the rest of
// the binary, and reloading fixtures would trip over that — see its own comment. access_token
// rows are prepared inline instead, per that fixture file's own rule.
func TestSweepPageTokens(t *testing.T) {
	ctx := t.Context()

	aged := &auth_model.AccessToken{UID: 2, Name: PageTokenName}
	require.NoError(t, auth_model.NewAccessToken(ctx, aged))
	fresh := &auth_model.AccessToken{UID: 2, Name: PageTokenName}
	require.NoError(t, auth_model.NewAccessToken(ctx, fresh))

	agedCreated := timeutil.TimeStamp(time.Now().Add(-25 * time.Hour).Unix())
	_, err := db.GetEngine(ctx).ID(aged.ID).Cols("created_unix").NoAutoTime().Update(&auth_model.AccessToken{CreatedUnix: agedCreated})
	require.NoError(t, err)

	deleted, err := SweepPageTokens(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)

	has, err := db.GetEngine(ctx).ID(aged.ID).Exist(new(auth_model.AccessToken))
	require.NoError(t, err)
	assert.False(t, has, "the aged token is gone")

	has, err = db.GetEngine(ctx).ID(fresh.ID).Exist(new(auth_model.AccessToken))
	require.NoError(t, err)
	assert.True(t, has, "the fresh token remains")
}
