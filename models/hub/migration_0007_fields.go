// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"context"

	"gitea.dev/models/db"
	planning_model "gitea.dev/models/planning"
)

func init() {
	RegisterMigration(&Migration{
		ID:          7,
		Description: "seed the instance's points custom field",
		Migrate:     migrateFields,
	})
}

// migrateFields seeds the instance scope's points field, once. Rerunning it inserts nothing:
// it does nothing once the instance scope already holds a field of that key, which also
// leaves an admin's own pre-upgrade points field untouched.
func migrateFields(ctx context.Context, e db.Engine) error {
	has, err := e.Where("repo_id = 0 AND org_id = 0 AND field_key = ?", "points").Exist(new(planning_model.Field))
	if err != nil || has {
		return err
	}
	_, err = e.Insert(&planning_model.Field{Key: "points", Label: "Points", Kind: "int"})
	return err
}
