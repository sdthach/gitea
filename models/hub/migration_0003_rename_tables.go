// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"context"
	"fmt"
	"strings"

	"gitea.dev/models/db"
	"gitea.dev/modules/json"
	"gitea.dev/modules/setting"
)

// tableRenames pairs every fork table's old name with its new one. hub_version is renamed
// separately, in Migrate itself, before this migration's own id can be read.
var tableRenames = [][2]string{
	{"delivery_environment", "deploy_environment"},
	{"delivery_deployment", "deploy_deployment"},
	{"delivery_approval", "deploy_review"},
	{"delivery_audit", "deploy_audit"},
	{"delivery_secret_scope", "deploy_secret_scope"},
}

func init() {
	RegisterMigration(&Migration{
		ID:          3,
		Description: "rename delivery_* tables to their deploy_* names",
		Migrate: func(ctx context.Context, e db.Engine) error {
			for _, r := range tableRenames {
				if err := migrateTablePair(e, r[0], r[1]); err != nil {
					return fmt.Errorf("migrate %s to %s: %w", r[0], r[1], err)
				}
			}
			return backfillEnvironmentColumns(e)
		},
	})
}

// migrateTablePair carries one old-named table onto its new name however Sync's own boot
// order leaves it: a plain rename when the new table was never created, or a row copy when
// Sync already created it empty ahead of Migrate. Either way it is idempotent — a table
// already gone, or a new table already holding rows, is left alone.
func migrateTablePair(e db.Engine, oldName, newName string) error {
	newExists, err := e.IsTableExist(newName)
	if err != nil {
		return err
	}
	if !newExists {
		return renameTable(e, oldName, newName)
	}
	oldExists, err := e.IsTableExist(oldName)
	if err != nil || !oldExists {
		return err
	}
	newHasRows, err := e.Table(newName).Exist()
	if err != nil {
		return err
	}
	if newHasRows {
		return dropTable(e, oldName)
	}
	if err := copyTableRows(e, oldName, newName); err != nil {
		return err
	}
	if err := resetPostgresSequence(e, newName); err != nil {
		return err
	}
	if newName == "deploy_environment" {
		if err := copyEnvironmentBackfill(e, oldName, newName); err != nil {
			return err
		}
	}
	return dropTable(e, oldName)
}

// copyTableRows moves every row from oldName to newName over their common column set,
// discovered from the connected dialect rather than hardcoded, so a column the rename would
// have renamed but the new model dropped is simply left behind.
func copyTableRows(e db.Engine, oldName, newName string) error {
	oldCols, err := tableColumns(e, oldName)
	if err != nil {
		return err
	}
	newCols, err := tableColumns(e, newName)
	if err != nil {
		return err
	}
	newColSet := make(map[string]bool, len(newCols))
	for _, c := range newCols {
		newColSet[strings.ToLower(c)] = true
	}
	common := make([]string, 0, len(oldCols))
	for _, c := range oldCols {
		if newColSet[strings.ToLower(c)] {
			common = append(common, c)
		}
	}
	if len(common) == 0 {
		return nil
	}
	quoted := make([]string, len(common))
	for i, c := range common {
		quoted[i] = quoteIdent(c)
	}
	colList := strings.Join(quoted, ", ")
	_, err = e.Exec(fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s", quoteIdent(newName), colList, colList, quoteIdent(oldName)))
	return err
}

// resetPostgresSequence advances newName's BIGSERIAL sequence past the highest id
// copyTableRows just inserted explicitly: those inserts never call nextval, so the sequence
// is still at 1 and the next insert without an id collides on the row copyTableRows added.
// MySQL's AUTO_INCREMENT and SQLite's ROWID both already track the max row without help.
// Reuses the setval shape models/db/sequence.go:64 already runs for the same reason.
func resetPostgresSequence(e db.Engine, name string) error {
	if !setting.Database.Type.IsPostgreSQL() {
		return nil
	}
	_, err := e.Exec(fmt.Sprintf("SELECT setval('%s_id_seq', COALESCE((SELECT MAX(id)+1 FROM %s), 1), false)", name, quoteIdent(name)))
	return err
}

// environmentReviewColumns is the pair of new environment columns migration 3 owns. It is
// declared here, not in models/deployments, because models/deployments already imports this
// package — Sync will also have added these columns on any boot where Sync ran against the
// new table name, and syncing the same columns again from here is a no-op.
type environmentReviewColumns struct {
	AdminsCanBypass bool     `xorm:"NOT NULL DEFAULT true"`
	DependsOn       []string `xorm:"JSON TEXT"`
}

func (*environmentReviewColumns) TableName() string { return "deploy_environment" }

// environmentPredecessorRow reads what the rename leaves behind: the old predecessor
// column, still physically present because the hub never drops a column. The copy path reads
// it from the old-named table instead, via an explicit Table() override — see
// copyEnvironmentBackfill.
type environmentPredecessorRow struct {
	ID          int64 `xorm:"pk autoincr"`
	Predecessor string
}

func (*environmentPredecessorRow) TableName() string { return "deploy_environment" }

// environmentBlockAdminOverrideRow reads the old bypass column the same way.
type environmentBlockAdminOverrideRow struct {
	ID                 int64 `xorm:"pk autoincr"`
	BlockAdminOverride bool
}

func (*environmentBlockAdminOverrideRow) TableName() string { return "deploy_environment" }

// normalizeDependencyNames applies the environment name spelling rule locally — this package
// cannot import models/deployments for NormalizeEnvironmentName without an import cycle back
// to itself — then drops every empty or duplicate name, so the backfill never writes
// depends_on: [""].
func normalizeDependencyNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// hasColumn probes for a column the portable way: every dialect refuses a query naming a
// column that is not there, and telling one dialect's failure from another's is not needed
// to answer the question.
func hasColumn(e db.Engine, table, column string) bool {
	_, err := e.Query(fmt.Sprintf("SELECT %s FROM %s LIMIT 1", column, table)) //nolint:gosec // table and column are fixed literals, never request input
	return err == nil
}

// backfillEnvironmentColumns carries the two environment fields whose meaning changed onto
// the columns replacing them: admins_can_bypass is the negation of block_admin_override, and
// depends_on is the single predecessor as a one-element list, or empty. Both old columns are
// read only if they are actually there — a table that reached deploy_environment through
// Sync rather than through the rename above never had them.
func backfillEnvironmentColumns(e db.Engine) error {
	exists, err := e.IsTableExist("deploy_environment")
	if err != nil || !exists {
		return err
	}
	if err := e.Sync(new(environmentReviewColumns)); err != nil {
		return err
	}

	if hasColumn(e, "deploy_environment", "block_admin_override") {
		if _, err := e.Exec("UPDATE deploy_environment SET admins_can_bypass = NOT block_admin_override"); err != nil {
			return err
		}
	}

	if !hasColumn(e, "deploy_environment", "predecessor") {
		return nil
	}
	rows := make([]*environmentPredecessorRow, 0, 8)
	if err := e.Find(&rows); err != nil {
		return err
	}
	for _, row := range rows {
		raw, err := json.Marshal(normalizeDependencyNames([]string{row.Predecessor}))
		if err != nil {
			return err
		}
		if _, err := e.Exec("UPDATE deploy_environment SET depends_on = ? WHERE id = ?", string(raw), row.ID); err != nil {
			return err
		}
	}
	return nil
}

// copyEnvironmentBackfill is backfillEnvironmentColumns' counterpart for the copy path: the
// old columns never reached newName's common column set, so they are read from oldName —
// still around, not yet dropped — instead of from newName itself.
func copyEnvironmentBackfill(e db.Engine, oldName, newName string) error {
	if err := e.Sync(new(environmentReviewColumns)); err != nil {
		return err
	}

	if hasColumn(e, oldName, "block_admin_override") {
		rows := make([]*environmentBlockAdminOverrideRow, 0, 8)
		if err := e.Table(oldName).Find(&rows); err != nil {
			return err
		}
		for _, row := range rows {
			if _, err := e.Exec(fmt.Sprintf("UPDATE %s SET admins_can_bypass = ? WHERE id = ?", quoteIdent(newName)),
				!row.BlockAdminOverride, row.ID); err != nil {
				return err
			}
		}
	}

	if !hasColumn(e, oldName, "predecessor") {
		return nil
	}
	rows := make([]*environmentPredecessorRow, 0, 8)
	if err := e.Table(oldName).Find(&rows); err != nil {
		return err
	}
	for _, row := range rows {
		raw, err := json.Marshal(normalizeDependencyNames([]string{row.Predecessor}))
		if err != nil {
			return err
		}
		if _, err := e.Exec(fmt.Sprintf("UPDATE %s SET depends_on = ? WHERE id = ?", quoteIdent(newName)), string(raw), row.ID); err != nil {
			return err
		}
	}
	return nil
}
