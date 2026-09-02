// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"
	"strings"

	"gitea.dev/models/db"
	"gitea.dev/modules/setting"

	"xorm.io/builder"
)

// Which environments take an unfinished build used to be one instance-wide list of names,
// [delivery] PRERELEASE_ENVIRONMENTS, defaulting to dev, qa and uat. It is a column now,
// because the names are the operator's and mean nothing to the fork.
//
// The key is read once, here, so an instance keeps refusing prereleases exactly where it
// refused them before the upgrade. Sync adds the column at its false default — every
// environment accepts anything — and this marks the rest.
func init() {
	RegisterMigration(&Migration{
		ID:          2,
		Description: "carry [delivery] PRERELEASE_ENVIRONMENTS onto delivery_environment.require_full_release",
		Migrate: func(ctx context.Context, e db.Engine) error {
			accepting := priorPrereleaseEnvironments()
			if len(accepting) == 0 {
				return nil
			}
			_, err := e.Where(builder.NotIn("name", accepting)).
				Cols("require_full_release").
				Update(&Environment{RequireFullRelease: true})
			return err
		},
	})
}

func priorPrereleaseEnvironments() []string {
	raw := "dev, qa, uat"
	if setting.CfgProvider != nil {
		if configured := setting.CfgProvider.Section(SettingSection).Key("PRERELEASE_ENVIRONMENTS").String(); strings.TrimSpace(configured) != "" {
			raw = configured
		}
	}
	out := make([]string, 0, 4)
	for name := range strings.SplitSeq(raw, ",") {
		if name = NormalizeEnvironmentName(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}
