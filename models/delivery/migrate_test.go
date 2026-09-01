// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"
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
// binary is never locked out by it (F6).
func TestPendingMigrationsRefusesANewerDatabase(t *testing.T) {
	_, err := pendingMigrations([]*Migration{{ID: 1, Description: "first", Migrate: noop}}, 9)
	require.Error(t, err)
	var hubErr *Error
	require.ErrorAs(t, err, &hubErr)
	assert.Contains(t, hubErr.Message, "version 9")
	assert.NotEmpty(t, hubErr.SuggestedAction, "every error carries a suggested next action (A21)")
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

// TestRegisteredMigrationsAreWellFormed guards the real set the binary ships.
func TestRegisteredMigrationsAreWellFormed(t *testing.T) {
	all := RegisteredMigrations()
	require.NotEmpty(t, all, "the fork ships at least one migration")
	require.NoError(t, validateMigrations(all))
	for _, m := range all {
		assert.NotEmpty(t, m.Description, "migration %d must describe itself", m.ID)
		assert.NotNil(t, m.Migrate, "migration %d must do something", m.ID)
	}
}

func ids(ms []*Migration) []int64 {
	out := make([]int64, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

// TestMigrateRecordsVersionInTheForksOwnTable exercises the runner against SQLite.
func TestMigrateRecordsVersionInTheForksOwnTable(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	require.NoError(t, Migrate(ctx))
	v, err := currentVersion(ctx)
	require.NoError(t, err)
	highest := RegisteredMigrations()[len(RegisteredMigrations())-1].ID
	assert.Equal(t, highest, v.Version)

	// Re-running is a no-op, so a restart is safe.
	require.NoError(t, Migrate(ctx))
	after, err := currentVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, v.ID, after.ID, "the fork keeps exactly one version row")
	assert.Equal(t, highest, after.Version)

	count, err := db.GetEngine(ctx).Count(new(Version))
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
}

// TestMigration0001LowercasesEnvironmentNames proves the shipped migration does its work.
func TestMigration0001LowercasesEnvironmentNames(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	require.NoError(t, db.Insert(ctx, &Environment{RepoID: 4242, Name: "PROD", ApprovalPolicy: PolicyNone, RequiredApprovals: 1}))

	var migration *Migration
	for _, m := range RegisteredMigrations() {
		if m.ID == 1 {
			migration = m
		}
	}
	require.NotNil(t, migration)
	require.NoError(t, migration.Migrate(ctx, db.GetEngine(ctx)))

	got := new(Environment)
	has, err := db.GetEngine(ctx).Where("repo_id = ?", 4242).Get(got)
	require.NoError(t, err)
	require.True(t, has)
	assert.Equal(t, "prod", got.Name)
}

// TestTheForkNeverTouchesGiteasSharedVersionRow is F6/SC 28 as a source check. Gitea
// log.Fatals when its shared `version` row exceeds what the binary knows, so registering
// into the shared list would permanently lock an older Gitea binary out of the database.
// The fork counts in its own `delivery_version` table and imports none of Gitea's migration
// machinery.
func TestTheForkNeverTouchesGiteasSharedVersionRow(t *testing.T) {
	assert.Equal(t, "delivery_version", new(Version).TableName())

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
					"%s contains %q; the fork counts in delivery_version and never in Gitea's shared version row (F6)", path, needle)
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
		filepath.Join(dir, "models", "delivery"),
		filepath.Join(dir, "services", "delivery"),
		filepath.Join(dir, "routers", "api", "delivery"),
		filepath.Join(dir, "routers", "web", "delivery"),
		filepath.Join(dir, "cmd", "gitea-delivery"),
	}
	for _, r := range roots {
		_, statErr := os.Stat(r)
		require.NoError(t, statErr, "fork package root %s is missing", r)
	}
	return roots
}

// TestInitMigratesAndSeeds covers the hub-mount entry point routers/init.go names. It is
// the whole of the fork's boot behaviour: migrate into the fork's own version table, then
// seed the default environment set.
func TestInitMigratesAndSeeds(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	_, err := db.GetEngine(ctx).Where("1=1").Delete(new(Environment))
	require.NoError(t, err)
	_, err = db.GetEngine(ctx).Where("1=1").Delete(new(Version))
	require.NoError(t, err)

	require.NoError(t, Init(ctx))

	v, err := currentVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, RegisteredMigrations()[len(RegisteredMigrations())-1].ID, v.Version, "Init migrates")

	count, err := db.GetEngine(ctx).Where("repo_id = ?", DefaultsRepoID).Count(new(Environment))
	require.NoError(t, err)
	assert.EqualValues(t, len(DefaultEnvironments), count, "Init seeds")

	// Booting twice is safe.
	require.NoError(t, Init(ctx))
	count, err = db.GetEngine(ctx).Where("repo_id = ?", DefaultsRepoID).Count(new(Environment))
	require.NoError(t, err)
	assert.EqualValues(t, len(DefaultEnvironments), count)
}
