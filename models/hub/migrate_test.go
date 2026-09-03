// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitea.dev/models/db"

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
