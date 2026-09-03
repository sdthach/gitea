// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"testing"

	"gitea.dev/models/db"
	hub_model "gitea.dev/models/hub"
	"gitea.dev/models/unittest"
	"gitea.dev/modules/setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegisteredMigrationsAreWellFormed guards the real set the binary ships.
func TestRegisteredMigrationsAreWellFormed(t *testing.T) {
	all := hub_model.RegisteredMigrations()
	require.NotEmpty(t, all, "the fork ships at least one migration")
	seen := map[int64]bool{}
	for _, m := range all {
		assert.Positive(t, m.ID, "migration ids are positive")
		assert.False(t, seen[m.ID], "migration id %d is registered only once", m.ID)
		seen[m.ID] = true
		assert.NotEmpty(t, m.Description, "migration %d must describe itself", m.ID)
		assert.NotNil(t, m.Migrate, "migration %d must do something", m.ID)
	}
}

// TestMigrateRecordsVersionInTheForksOwnTable exercises the runner against SQLite.
func TestMigrateRecordsVersionInTheForksOwnTable(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	require.NoError(t, hub_model.Migrate(ctx))
	v := new(hub_model.Version)
	has, err := db.GetEngine(ctx).Where("1=1").Get(v)
	require.NoError(t, err)
	require.True(t, has)
	highest := hub_model.RegisteredMigrations()[len(hub_model.RegisteredMigrations())-1].ID
	assert.Equal(t, highest, v.Version)

	// Re-running is a no-op, so a restart is safe.
	require.NoError(t, hub_model.Migrate(ctx))
	after := new(hub_model.Version)
	has, err = db.GetEngine(ctx).Where("1=1").Get(after)
	require.NoError(t, err)
	require.True(t, has)
	assert.Equal(t, v.ID, after.ID, "the fork keeps exactly one version row")
	assert.Equal(t, highest, after.Version)

	count, err := db.GetEngine(ctx).Count(new(hub_model.Version))
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
}

// TestMigration0001LowercasesEnvironmentNames proves the shipped migration does its work.
func TestMigration0001LowercasesEnvironmentNames(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	require.NoError(t, db.Insert(ctx, &Environment{RepoID: 4242, Name: "PROD", ApprovalPolicy: PolicyNone, RequiredApprovals: 1}))

	migration := migrationByID(t, 1)
	require.NoError(t, migration.Migrate(ctx, db.GetEngine(ctx)))

	got := new(Environment)
	has, err := db.GetEngine(ctx).Where("repo_id = ?", 4242).Get(got)
	require.NoError(t, err)
	require.True(t, has)
	assert.Equal(t, "prod", got.Name)
}

// TestMigration0002CarriesThePrereleaseNameListOntoTheColumn proves the upgrade keeps an
// instance refusing prereleases exactly where the old name list refused them.
func TestMigration0002CarriesThePrereleaseNameListOntoTheColumn(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	previous := setting.CfgProvider
	t.Cleanup(func() { setting.CfgProvider = previous })
	cfg, err := setting.NewConfigProviderFromData("[delivery]\nPRERELEASE_ENVIRONMENTS = sandbox\n")
	require.NoError(t, err)
	setting.CfgProvider = cfg

	for _, name := range []string{"sandbox", "live"} {
		require.NoError(t, db.Insert(ctx, &Environment{
			RepoID: 4242, Name: name, ApprovalPolicy: PolicyNone, RequiredApprovals: 1,
		}))
	}

	require.NoError(t, migrationByID(t, 2).Migrate(ctx, db.GetEngine(ctx)))

	sandbox, err := GetEnvironment(ctx, 4242, "sandbox")
	require.NoError(t, err)
	assert.False(t, sandbox.RequireFullRelease, "an environment the key named still takes prereleases")

	live, err := GetEnvironment(ctx, 4242, "live")
	require.NoError(t, err)
	assert.True(t, live.RequireFullRelease, "every other environment now asks for finished releases")
}

func migrationByID(t *testing.T, id int64) *hub_model.Migration {
	t.Helper()
	for _, m := range hub_model.RegisteredMigrations() {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("no migration with id %d is registered", id)
	return nil
}

// TestInitMigratesAndSeeds covers the hub-mount entry point routers/init.go names. It is
// the whole of the fork's boot behaviour: migrate into the fork's own version table, then
// seed whatever set the operator configured.
