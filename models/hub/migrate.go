// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"context"
	"fmt"
	"sort"

	"gitea.dev/models/db"
	"gitea.dev/modules/log"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/timeutil"
)

// Version is the fork's OWN version row. The fork never touches Gitea's shared
// `version` row: Gitea log.Fatals when that row exceeds what the binary knows, so
// registering into the shared list would permanently lock an older Gitea binary out of
// the database.
type Version struct {
	ID          int64              `xorm:"pk autoincr"`
	Version     int64              `xorm:"NOT NULL DEFAULT 0"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated NOT NULL"`
}

func (*Version) TableName() string { return "hub_version" }

func init() {
	db.RegisterModel(new(Version))
}

// Migration is one forward-only, additive step. Migrations create fork tables and add fork
// columns; they never rewrite, rename or drop an upstream table or column, and they never
// remove a column — Sync does not drop one and the dialects disagree on how.
type Migration struct {
	ID          int64 // matches the numeric prefix of the file that registers it
	Description string
	Migrate     func(ctx context.Context, e db.Engine) error
}

// migrations is appended to by each migration file's own init(), one file each, so there
// is no shared array for two rebased commits to conflict on.
var migrations []*Migration

// RegisterMigration is called from the init() of the file that defines the migration.
func RegisterMigration(m *Migration) { migrations = append(migrations, m) }

// RegisteredMigrations returns the registered set sorted by ID.
func RegisteredMigrations() []*Migration {
	out := make([]*Migration, len(migrations))
	copy(out, migrations)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// validateMigrations refuses a set two files could have collided on. Duplicate or
// non-positive IDs make "version N is applied" ambiguous.
func validateMigrations(all []*Migration) error {
	seen := map[int64]string{}
	for _, m := range all {
		if m.ID <= 0 {
			return &Error{
				Message:         fmt.Sprintf("delivery migration %q has non-positive id %d", m.Description, m.ID),
				SuggestedAction: "Number the migration from 1 upwards, matching its file's numeric prefix.",
			}
		}
		if prev, dup := seen[m.ID]; dup {
			return &Error{
				Message:         fmt.Sprintf("delivery migrations %q and %q both claim id %d", prev, m.Description, m.ID),
				SuggestedAction: "Renumber the later migration to the next free id and rename its file to match.",
			}
		}
		seen[m.ID] = m.Description
	}
	return nil
}

// pendingMigrations selects what still has to run. It is pure so the ordering, the
// skipping and the newer-database refusal are testable without a database.
func pendingMigrations(all []*Migration, current int64) ([]*Migration, error) {
	sorted := make([]*Migration, len(all))
	copy(sorted, all)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	if err := validateMigrations(sorted); err != nil {
		return nil, err
	}

	var highest int64
	if len(sorted) > 0 {
		highest = sorted[len(sorted)-1].ID
	}
	if current > highest {
		return nil, &Error{
			Message: fmt.Sprintf("the database is at delivery schema version %d but this binary knows only %d",
				current, highest),
			SuggestedAction: "Start the newer fork build against this database, or restore the backup taken before the upgrade. Gitea's own schema is untouched, so a stock Gitea binary still starts.",
		}
	}

	pending := make([]*Migration, 0, len(sorted))
	for _, m := range sorted {
		if m.ID > current {
			pending = append(pending, m)
		}
	}
	return pending, nil
}

// renameTable performs one dialect-safe table rename, doing nothing when old does not exist
// or new already does — so callers can run it unconditionally on every boot rather than
// gating it on the migration version, which is what the version row's own rename needs
// before that version can even be read.
func renameTable(e db.Engine, oldName, newName string) error {
	has, err := e.IsTableExist(oldName)
	if err != nil || !has {
		return err
	}
	has, err = e.IsTableExist(newName)
	if err != nil || has {
		return err
	}
	if setting.Database.Type == "mysql" {
		_, err = e.Exec(fmt.Sprintf("RENAME TABLE `%s` TO `%s`", oldName, newName))
	} else {
		_, err = e.Exec(fmt.Sprintf("ALTER TABLE `%s` RENAME TO `%s`", oldName, newName))
	}
	return err
}

// quoteIdent quotes an identifier the way the connected dialect expects: postgres takes
// double quotes, mysql and sqlite both accept backticks.
func quoteIdent(name string) string {
	if setting.Database.Type.IsPostgreSQL() {
		return `"` + name + `"`
	}
	return "`" + name + "`"
}

// dropTable drops a table if it exists, the same statement on every dialect the fork
// supports.
func dropTable(e db.Engine, name string) error {
	_, err := e.Exec("DROP TABLE IF EXISTS " + quoteIdent(name))
	return err
}

// tableColumns lists a table's column names as the connected dialect reports them, so a
// migration copying rows between two tables only ever moves columns that exist on both.
func tableColumns(e db.Engine, table string) ([]string, error) {
	var query string
	switch {
	case setting.Database.Type.IsMySQL():
		query = "SELECT column_name AS name FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ?"
	case setting.Database.Type.IsPostgreSQL():
		query = "SELECT column_name AS name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ?"
	default: // sqlite
		query = "SELECT name FROM pragma_table_info(?)"
	}
	rows, err := e.Query(query, table)
	if err != nil {
		return nil, err
	}
	cols := make([]string, 0, len(rows))
	for _, row := range rows {
		cols = append(cols, string(row["name"]))
	}
	return cols, nil
}

// oldVersionRow reads the version row under its pre-rename name. Sync creates hub_version —
// registered like every other fork model — before Migrate ever runs, so on a real upgrade the
// row Migrate needs is still sitting in delivery_version under the old name.
type oldVersionRow struct {
	ID          int64              `xorm:"pk autoincr"`
	Version     int64              `xorm:"NOT NULL DEFAULT 0"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated NOT NULL"`
}

func (*oldVersionRow) TableName() string { return "delivery_version" }

// migrateVersionTable carries the fork's own version number across the rename however it
// gets there. An install still running an older binary already renamed everything before this
// ordering existed, so delivery_version is the only one of the two Sync ever created — rename
// it. An install whose Sync already ran under the corrected boot order created hub_version
// empty while the real row is still sitting in delivery_version — copy it over.
func migrateVersionTable(ctx context.Context, e db.Engine) error {
	oldExists, err := e.IsTableExist("delivery_version")
	if err != nil || !oldExists {
		return err
	}
	hubExists, err := e.IsTableExist("hub_version")
	if err != nil {
		return err
	}
	if !hubExists {
		return renameTable(e, "delivery_version", "hub_version")
	}
	hubHasRow, err := e.Where("1=1").Exist(new(Version))
	if err != nil {
		return err
	}
	if !hubHasRow {
		old := new(oldVersionRow)
		has, err := e.Where("1=1").Get(old)
		if err != nil {
			return err
		}
		if has {
			if err := db.Insert(ctx, &Version{Version: old.Version, UpdatedUnix: old.UpdatedUnix}); err != nil {
				return err
			}
		}
	}
	return dropTable(e, "delivery_version")
}

// currentVersion reads the fork's own version row, creating it at 0 on a fresh install so
// a fresh install and an upgrade from stock converge on the same state.
func currentVersion(ctx context.Context) (*Version, error) {
	v := new(Version)
	has, err := db.GetEngine(ctx).Where("1=1").Get(v)
	if err != nil {
		return nil, err
	}
	if has {
		return v, nil
	}
	v = &Version{Version: 0}
	if err := db.Insert(ctx, v); err != nil {
		return nil, err
	}
	return v, nil
}

// Migrate applies every pending fork migration. The tables themselves are created by
// Gitea's own Sync over the models RegisterModel received, so a migration exists only
// for work Sync cannot do.
func Migrate(ctx context.Context) error {
	// The version row is carried across the rename here, before its own version number is
	// read — a migration keyed by that number cannot rename the table the number lives in.
	if err := migrateVersionTable(ctx, db.GetEngine(ctx)); err != nil {
		return fmt.Errorf("migrate delivery_version to hub_version: %w", err)
	}
	v, err := currentVersion(ctx)
	if err != nil {
		return err
	}
	pending, err := pendingMigrations(RegisteredMigrations(), v.Version)
	if err != nil {
		return err
	}
	for _, m := range pending {
		log.Info("delivery: migrating to schema version %d: %s", m.ID, m.Description)
		if err := m.Migrate(ctx, db.GetEngine(ctx)); err != nil {
			return fmt.Errorf("delivery migration %d (%s): %w", m.ID, m.Description, err)
		}
		v.Version = m.ID
		if _, err := db.GetEngine(ctx).ID(v.ID).Cols("version", "updated_unix").Update(v); err != nil {
			return fmt.Errorf("delivery migration %d: record version: %w", m.ID, err)
		}
	}
	return nil
}
