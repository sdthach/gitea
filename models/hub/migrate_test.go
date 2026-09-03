// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitea.dev/models/db"
	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noop(context.Context, db.Engine) error { return nil }

func TestPendingMigrationsOrdersAndSkips(t *testing.T) {
	all := []*Migration{
		{ID: 3, Description: "third", Migrate: noop},
		{ID: 1, Description: "first", Migrate: noop},
		{ID: 2, Description: "second", Migrate: noop},
	}

	pending, err := pendingMigrations(all, 0)
	require.NoError(t, err)
	require.Len(t, pending, 3)
	assert.Equal(t, []int64{1, 2, 3}, ids(pending), "migrations run in id order however they registered")

	pending, err = pendingMigrations(all, 2)
	require.NoError(t, err)
	assert.Equal(t, []int64{3}, ids(pending), "an already-applied migration is not re-run")

	pending, err = pendingMigrations(all, 3)
	require.NoError(t, err)
	assert.Empty(t, pending, "a fully migrated database has nothing pending")
}

// TestPendingMigrationsRefusesANewerDatabase is the fork's own version guard. It refuses in
// the fork's own table; Gitea's shared version row is never involved, so an older Gitea
// binary is never locked out by it.
func TestPendingMigrationsRefusesANewerDatabase(t *testing.T) {
	_, err := pendingMigrations([]*Migration{{ID: 1, Description: "first", Migrate: noop}}, 9)
	require.Error(t, err)
	var hubErr *Error
	require.ErrorAs(t, err, &hubErr)
	assert.Contains(t, hubErr.Message, "version 9")
	assert.NotEmpty(t, hubErr.SuggestedAction, "every error carries a suggested next action")
}

func TestValidateMigrationsRefusesDuplicateAndZeroIDs(t *testing.T) {
	_, err := pendingMigrations([]*Migration{
		{ID: 1, Description: "a", Migrate: noop},
		{ID: 1, Description: "b", Migrate: noop},
	}, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id 1")

	_, err = pendingMigrations([]*Migration{{ID: 0, Description: "unnumbered", Migrate: noop}}, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-positive")
}

func ids(ms []*Migration) []int64 {
	out := make([]int64, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

// TestTheForkNeverTouchesGiteasSharedVersionRow is a source check. Gitea
// log.Fatals when its shared `version` row exceeds what the binary knows, so registering
// into the shared list would permanently lock an older Gitea binary out of the database.
// The fork counts in its own `hub_version` table and imports none of Gitea's migration
// machinery.
func TestTheForkNeverTouchesGiteasSharedVersionRow(t *testing.T) {
	assert.Equal(t, "hub_version", new(Version).TableName())

	// Each needle is a way a fork file could reach Gitea's shared version row or its
	// migration list.
	forbidden := []string{
		"gitea.dev/modelmigration",
		"modelmigration.",
		`Table("version")`,
		`return "version"`,
	}
	scanned := 0
	for _, dir := range forkPackageRoots(t) {
		require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			scanned++
			for _, needle := range forbidden {
				assert.NotContains(t, string(raw), needle,
					"%s contains %q; the fork counts in delivery_version and never in Gitea's shared version row", path, needle)
			}
			return nil
		}))
	}
	assert.Greater(t, scanned, 10, "the scan must actually have read the fork's files")
}

// forkPackageRoots is every directory the fork owns Go code in.
func forkPackageRoots(t *testing.T) []string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for range 8 {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	roots := []string{
		filepath.Join(dir, "models", "hub"),
		filepath.Join(dir, "models", "deployments"),
		filepath.Join(dir, "services", "hub"),
		filepath.Join(dir, "services", "planning"),
		filepath.Join(dir, "services", "deployments"),
		filepath.Join(dir, "routers", "api", "hub"),
		filepath.Join(dir, "routers", "api", "planning"),
		filepath.Join(dir, "routers", "api", "deployments"),
		filepath.Join(dir, "routers", "web", "hub"),
		filepath.Join(dir, "routers", "web", "planning"),
		filepath.Join(dir, "routers", "web", "deployments"),
		filepath.Join(dir, "cmd", "hubcli"),
		filepath.Join(dir, "cmd", "gitea-planning"),
		filepath.Join(dir, "cmd", "gitea-deployments"),
	}
	for _, r := range roots {
		_, statErr := os.Stat(r)
		require.NoError(t, statErr, "fork package root %s is missing", r)
	}
	return roots
}

// oldNamedTables is every table migration 3 renames, plus the version row renamed in
// Migrate itself, paired with the schema a pre-rename install actually had.
var oldNamedTables = []struct {
	old, new, schema, row string
}{
	{
		// version 2: migrations 1 and 2 are already applied, as they are on any real install
		// that reaches this rename — they shipped, and ran, before it. Only migration 3,
		// the rename itself, is pending; it is the only migration that ever runs against a
		// table still under its delivery_* name.
		"delivery_version", "hub_version",
		"id INTEGER PRIMARY KEY, version INTEGER NOT NULL DEFAULT 0, updated_unix INTEGER NOT NULL DEFAULT 0",
		"(1, 2, 1788220800)",
	},
	{
		"delivery_environment", "deploy_environment",
		"id INTEGER PRIMARY KEY, repo_id INTEGER NOT NULL DEFAULT 0, name TEXT NOT NULL, " +
			"approval_policy TEXT NOT NULL DEFAULT 'none', required_approvals INTEGER NOT NULL DEFAULT 1, " +
			"predecessor TEXT NOT NULL DEFAULT '', block_admin_override INTEGER NOT NULL DEFAULT 0",
		"(1, 42, 'prod', 'none', 1, 'staging', 1)",
	},
	{
		"delivery_deployment", "deploy_deployment",
		"id INTEGER PRIMARY KEY, repo_id INTEGER NOT NULL, environment TEXT NOT NULL, release_tag TEXT NOT NULL",
		"(1, 42, 'prod', 'v1.0')",
	},
	{
		"delivery_approval", "deploy_review",
		"id INTEGER PRIMARY KEY, repo_id INTEGER NOT NULL, environment TEXT NOT NULL, run_id INTEGER NOT NULL, job_id INTEGER NOT NULL",
		"(1, 42, 'prod', 9, 5)",
	},
	{
		"delivery_audit", "deploy_audit",
		"id INTEGER PRIMARY KEY, repo_id INTEGER NOT NULL, event TEXT NOT NULL, environment TEXT NOT NULL",
		"(1, 42, 'succeeded', 'prod')",
	},
	{
		"delivery_secret_scope", "deploy_secret_scope",
		"id INTEGER PRIMARY KEY, repo_id INTEGER NOT NULL, name TEXT NOT NULL, environment TEXT NOT NULL",
		"(1, 42, 'DEPLOY_KEY', 'prod')",
	},
}

// TestMigrateRenamesTheOldTables proves the rename Migrate does before every registered
// migration even runs: an install still on the delivery_* names ends up on the deploy_*
// names (hub_version excepted) with its rows intact, and a second run is a no-op.
func TestMigrateRenamesTheOldTables(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	e := db.GetEngine(ctx)

	// Sync has already created every table under its current (new) name; drop them so the
	// scenario under test — an install still on the old names — is the one Migrate sees.
	for _, tt := range oldNamedTables {
		_, err := e.Exec("DROP TABLE IF EXISTS " + tt.new)
		require.NoError(t, err)
		_, err = e.Exec(fmt.Sprintf("CREATE TABLE %s (%s)", tt.old, tt.schema))
		require.NoError(t, err)
		_, err = e.Exec(fmt.Sprintf("INSERT INTO %s VALUES %s", tt.old, tt.row))
		require.NoError(t, err)
	}

	require.NoError(t, Migrate(ctx))

	for _, tt := range oldNamedTables {
		oldExists, err := e.IsTableExist(tt.old)
		require.NoError(t, err)
		assert.False(t, oldExists, "%s must be gone once %s exists", tt.old, tt.new)

		counted, err := e.Query("SELECT COUNT(*) AS c FROM " + tt.new)
		require.NoError(t, err)
		require.Len(t, counted, 1)
		assert.Equal(t, "1", string(counted[0]["c"]), "%s must keep the row that was in %s", tt.new, tt.old)
	}

	// migration 3's own backfill: admins_can_bypass is the negation of the retired
	// block_admin_override, and depends_on is the single predecessor as a one-element list.
	backfilled, err := e.Query("SELECT admins_can_bypass, depends_on FROM deploy_environment WHERE id = 1")
	require.NoError(t, err)
	require.Len(t, backfilled, 1)
	assert.Equal(t, "0", string(backfilled[0]["admins_can_bypass"]), "block_admin_override was 1, so admins_can_bypass is 0")
	assert.JSONEq(t, `["staging"]`, string(backfilled[0]["depends_on"]))

	// Re-running is a no-op: every old table is already gone, so the guard skips every rename.
	require.NoError(t, Migrate(ctx))
}
