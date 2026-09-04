// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"context"
	"strings"

	"gitea.dev/models/db"
	hub_model "gitea.dev/models/hub"
	"gitea.dev/modules/setting"

	"xorm.io/builder"
)

// Which environments take an unfinished build used to be one instance-wide list of names,
// [delivery] PRERELEASE_ENVIRONMENTS, defaulting to dev, qa and uat. It is a column now,
// because the names are the operator's and mean nothing to the fork. [deployments]
// RELEASES_ONLY_ENVIRONMENTS replaced it on the split: this reads that key first, naming the
// environments to mark releases-only directly, and falls back to the old key and section for
// an app.ini written before the split, whose list is the opposite sense — the environments
// that still accept a prerelease.
//
// Either key is read once, here, so an instance keeps refusing prereleases exactly where it
// refused them before the upgrade. Sync adds the column at its false default — every
// environment accepts anything — and this marks the rest.
func init() {
	hub_model.RegisterMigration(&hub_model.Migration{
		ID:          2,
		Description: "carry [deployments] RELEASES_ONLY_ENVIRONMENTS, or the old [delivery] PRERELEASE_ENVIRONMENTS, onto deploy_environment.releases_only",
		Migrate: func(ctx context.Context, e db.Engine) error {
			if releasesOnly := priorReleasesOnlyEnvironments(); len(releasesOnly) > 0 {
				_, err := e.In("name", releasesOnly).Cols("releases_only").Update(&Environment{ReleasesOnly: true})
				return err
			}
			accepting := priorPrereleaseEnvironments()
			if len(accepting) == 0 {
				return nil
			}
			_, err := e.Where(builder.NotIn("name", accepting)).
				Cols("releases_only").
				Update(&Environment{ReleasesOnly: true})
			return err
		},
	})
}

// legacyPrereleaseSection is [delivery], frozen here rather than read through
// SettingSection: this migration carries forward what an OLD config said before the
// [delivery] section was split by area, so it always means the section that key lived in,
// whatever SettingSection is renamed to next.
const legacyPrereleaseSection = "delivery"

// priorReleasesOnlyEnvironments reads the documented [deployments] RELEASES_ONLY_ENVIRONMENTS
// key: the environments this marks releases-only directly, spelled the same way the column
// itself is.
func priorReleasesOnlyEnvironments() []string {
	if setting.CfgProvider == nil {
		return nil
	}
	return splitEnvironmentNames(setting.CfgProvider.Section(SettingSection).Key("RELEASES_ONLY_ENVIRONMENTS").String())
}

func priorPrereleaseEnvironments() []string {
	raw := "dev, qa, uat"
	if setting.CfgProvider != nil {
		if configured := setting.CfgProvider.Section(legacyPrereleaseSection).Key("PRERELEASE_ENVIRONMENTS").String(); strings.TrimSpace(configured) != "" {
			raw = configured
		}
	}
	return splitEnvironmentNames(raw)
}

func splitEnvironmentNames(raw string) []string {
	out := make([]string, 0, 4)
	for name := range strings.SplitSeq(raw, ",") {
		if name = NormalizeEnvironmentName(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}
