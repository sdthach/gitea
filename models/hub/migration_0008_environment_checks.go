// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"context"
	"slices"

	"gitea.dev/models/db"
)

func init() {
	RegisterMigration(&Migration{
		ID:          8,
		Description: "backfill deploy_environment.required_status_contexts for rows Sync left NULL",
		Migrate:     migrateEnvironmentChecks,
	})
}

// migrateEnvironmentChecks normalizes required_status_contexts to an empty JSON array on any
// row Sync created the column NULL on. auto_promote, wait_minutes and exclusive_lock get their
// zero value from the column's own NOT NULL DEFAULT at Sync time; deploy_window stays NULL,
// which is its own valid "always open" value, so neither needs a backfill here.
//
// Re-running finds nothing left to update: the WHERE clause is exactly what makes the first
// run's own work invisible to the second. The column-existence check is for a database that
// reaches this migration before Sync has added the column — Sync runs on every boot before
// Migrate does, so that never happens on a real install, but a test exercising Migrate alone
// over a hand-built schema can.
func migrateEnvironmentChecks(_ context.Context, e db.Engine) error {
	cols, err := tableColumns(e, "deploy_environment")
	if err != nil || !slices.Contains(cols, "required_status_contexts") {
		return err
	}
	_, err = e.Exec("UPDATE deploy_environment SET required_status_contexts = '[]' WHERE required_status_contexts IS NULL")
	return err
}
