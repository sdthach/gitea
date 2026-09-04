// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// package hub_test, not hub, so this test can import models/deployments — models/deployments
// already imports models/hub, and the reverse from an internal test file would be a cycle.
package hub_test

import (
	"fmt"
	"testing"

	"gitea.dev/models/db"
	deployments_model "gitea.dev/models/deployments"
	hub_model "gitea.dev/models/hub"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oldNamedTables is the old-named schema a pre-rename install actually had, columns spelled
// exactly as they were before the split renamed both the columns and the table — see
// git history at models/delivery/environment.go — so the backfill this test exercises reads
// real column names, not a guess at them.
var oldNamedTables = []struct {
	old, new, schema, row string
}{
	{
		// version 2: migrations 1 and 2 are already applied, as they are on any real install
		// that reaches this rename. Migration 3, the rename itself, and every later one are pending.
		"delivery_version", "hub_version",
		"id INTEGER PRIMARY KEY, version INTEGER NOT NULL DEFAULT 0, updated_unix INTEGER NOT NULL DEFAULT 0",
		"(1, 2, 1788220800)",
	},
	{
		"delivery_environment", "deploy_environment",
		"id INTEGER PRIMARY KEY, repo_id INTEGER NOT NULL DEFAULT 0, name TEXT NOT NULL, " +
			"approval_policy TEXT NOT NULL DEFAULT 'none', required_approvals INTEGER NOT NULL DEFAULT 1, " +
			"predecessor TEXT NOT NULL DEFAULT '', require_full_release INTEGER NOT NULL DEFAULT 0, " +
			"block_admin_override INTEGER NOT NULL DEFAULT 0, " +
			"created_unix INTEGER NOT NULL, updated_unix INTEGER NOT NULL",
		"(1, 42, 'prod', 'none', 1, 'staging', 1, 1, 1788220800, 1788220800)",
	},
	{
		"delivery_deployment", "deploy_deployment",
		"id INTEGER PRIMARY KEY, repo_id INTEGER NOT NULL, environment TEXT NOT NULL, " +
			"release_tag TEXT NOT NULL, status TEXT NOT NULL, created_unix INTEGER NOT NULL",
		"(1, 42, 'prod', 'v1.0', 'succeeded', 1788220800)",
	},
	{
		"delivery_approval", "deploy_review",
		"id INTEGER PRIMARY KEY, repo_id INTEGER NOT NULL, environment TEXT NOT NULL, " +
			"run_id INTEGER NOT NULL, job_id INTEGER NOT NULL, created_unix INTEGER NOT NULL",
		"(1, 42, 'prod', 9, 5, 1788220800)",
	},
	{
		"delivery_audit", "deploy_audit",
		"id INTEGER PRIMARY KEY, repo_id INTEGER NOT NULL, event TEXT NOT NULL, environment TEXT NOT NULL, " +
			"occurred_unix INTEGER NOT NULL, source TEXT NOT NULL, created_unix INTEGER NOT NULL",
		"(1, 42, 'succeeded', 'prod', 1788220800, 'ui', 1788220800)",
	},
	{
		// secret_name, not name: the field was never renamed across the split, so the
		// physical column is what SecretName has always mapped to.
		"delivery_secret_scope", "deploy_secret_scope",
		"id INTEGER PRIMARY KEY, repo_id INTEGER NOT NULL, secret_name TEXT NOT NULL, environment TEXT NOT NULL, " +
			"created_unix INTEGER NOT NULL, updated_unix INTEGER NOT NULL",
		"(1, 42, 'DEPLOY_KEY', 'prod', 1788220800, 1788220800)",
	},
}

// TestMigrateCopiesRowsWhenSyncCreatedTheNewTablesFirst covers the boot order
// common.InitDBEngine actually runs: Sync creates every registered table under its new name,
// from the shipped models themselves, before hub_service.Init ever calls Migrate. An upgrade
// therefore meets both an old table full of rows and a new table Sync already created empty,
// and Migrate must copy the rows across rather than leave them orphaned behind the guard that
// skips a new table that already exists.
//
// unittest.PrepareTestDatabase is deliberately not called here: this test's rows all come
// from the raw SQL below, and reloading fixtures would trip over the old-named tables this
// test creates for itself.
func TestMigrateCopiesRowsWhenSyncCreatedTheNewTablesFirst(t *testing.T) {
	ctx := t.Context()
	e := db.GetEngine(ctx)

	// The old tables, still under their delivery_* names and holding the real rows an
	// upgrade must carry forward. oldNamedTables' delivery_version row is already at
	// version 2, so migration 3 is the only one pending.
	for _, tt := range oldNamedTables {
		_, err := e.Exec("DROP TABLE IF EXISTS " + tt.new)
		require.NoError(t, err)
		_, err = e.Exec("DROP TABLE IF EXISTS " + tt.old)
		require.NoError(t, err)
		_, err = e.Exec(fmt.Sprintf("CREATE TABLE %s (%s)", tt.old, tt.schema))
		require.NoError(t, err)
		_, err = e.Exec(fmt.Sprintf("INSERT INTO %s VALUES %s", tt.old, tt.row))
		require.NoError(t, err)
	}

	// Sync, run before Migrate under the corrected boot order, creates every new table from
	// the shipped models — not a stand-in with a handful of columns — so a copy that only
	// works against an approximate schema is caught here.
	require.NoError(t, e.Sync(new(hub_model.Version), new(deployments_model.Environment),
		new(deployments_model.Deployment), new(deployments_model.Review),
		new(deployments_model.AuditEvent), new(deployments_model.SecretScope)))

	require.NoError(t, hub_model.Migrate(ctx))

	for _, tt := range oldNamedTables {
		oldExists, err := e.IsTableExist(tt.old)
		require.NoError(t, err)
		assert.False(t, oldExists, "%s must be gone once its row is copied into %s", tt.old, tt.new)

		counted, err := e.Query("SELECT COUNT(*) AS c FROM " + tt.new)
		require.NoError(t, err)
		require.Len(t, counted, 1)
		assert.Equal(t, "1", string(counted[0]["c"]), "%s must hold the row copied from %s", tt.new, tt.old)
	}

	// migration 3's own backfill: admins_can_bypass is the negation of the retired
	// block_admin_override, and depends_on is the single predecessor as a one-element list.
	// require_full_release has no replacement column under that name, so it is simply left
	// behind rather than crashing the copy.
	backfilled, err := e.Query("SELECT admins_can_bypass, depends_on, releases_only FROM deploy_environment WHERE id = 1")
	require.NoError(t, err)
	require.Len(t, backfilled, 1)
	assert.Equal(t, "0", string(backfilled[0]["admins_can_bypass"]), "block_admin_override was 1, so admins_can_bypass is 0")
	assert.JSONEq(t, `["staging"]`, string(backfilled[0]["depends_on"]))
	assert.Equal(t, "0", string(backfilled[0]["releases_only"]), "require_full_release names no column the copy carries forward")

	version := new(hub_model.Version)
	has, err := e.Where("1=1").Get(version)
	require.NoError(t, err)
	require.True(t, has)
	assert.Equal(t, int64(8), version.Version, "the version row copied from delivery_version survived, migrated to 8")

	// Re-running is a no-op: every old table is already gone.
	require.NoError(t, hub_model.Migrate(ctx))
}
