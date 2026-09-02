// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"

	"gitea.dev/models/db"
)

// Environment names are identifiers, compared and stored lower-cased
// (NormalizeEnvironmentName). Rows written before that rule was enforced are normalized
// here; Sync cannot do this, which is what makes it a migration rather than a struct change.
func init() {
	RegisterMigration(&Migration{
		ID:          1,
		Description: "lower-case delivery_environment.name",
		Migrate: func(ctx context.Context, e db.Engine) error {
			_, err := e.Exec("UPDATE delivery_environment SET name = LOWER(name) WHERE name <> LOWER(name)")
			return err
		},
	})
}
