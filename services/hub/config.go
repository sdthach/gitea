// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package hub holds the shared service layer: settings, the query grammar, and CLI
// generation. It reads its own configuration section rather than adding a field to an
// upstream settings struct, which would be a second edit to an upstream file.
package hub

import (
	deployments_model "gitea.dev/models/deployments"
	"gitea.dev/modules/setting"
)

// SettingSection is the app.ini section the fork reads for deployment settings. It is
// declared once, beside the model that also reads it.
const SettingSection = deployments_model.SettingSection

// PlanningSettingSection is the app.ini section the fork reads for planning settings. There
// is no models/planning package to declare it beside, since nothing there persists a row.
const PlanningSettingSection = "planning"

// legacySettingSection is the single section every fork setting lived under before it was
// split by area. Reads fall back to it for one release so an app.ini written before the
// split keeps working.
const legacySettingSection = "delivery"

// configKey reads key from section, falling back to legacySettingSection when the new
// section does not declare it.
func configKey(section, key string) setting.ConfigKey {
	if setting.CfgProvider.Section(section).HasKey(key) {
		return setting.CfgProvider.Section(section).Key(key)
	}
	return setting.CfgProvider.Section(legacySettingSection).Key(key)
}

// PlanningPagesEnabled reports whether the fork's Projects pages (board, roadmap) are
// served. It mirrors reqMilestonesDashboardPageEnabled's shape, so the feature can be
// switched off with one app.ini key:
//
//	[planning]
//	ENABLE_PAGES = false
//
// It defaults to true, so a fork build behaves the same whether or not the section exists.
// [delivery] ENABLE_PAGES is read as a fallback for an app.ini written before the split.
func PlanningPagesEnabled() bool {
	if setting.CfgProvider == nil {
		return true
	}
	return configKey(PlanningSettingSection, "ENABLE_PAGES").MustBool(true)
}

// DeploymentsPagesEnabled reports whether the fork's Deployments pages (environments,
// deployments, reviews, insights) are served:
//
//	[deployments]
//	ENABLE_PAGES = false
//
// It defaults to true, so a fork build behaves the same whether or not the section exists.
// [delivery] ENABLE_PAGES is read as a fallback for an app.ini written before the split.
func DeploymentsPagesEnabled() bool {
	if setting.CfgProvider == nil {
		return true
	}
	return configKey(SettingSection, "ENABLE_PAGES").MustBool(true)
}

// SwimlanesEnabled reports whether Gitea's own repository project page offers the fork's
// lane grouping:
//
//	[deployments]
//	ENABLE_SWIMLANES = true
//
// It defaults to FALSE, unlike the pages: this one changes a page the fork does not own, so
// a build that has not asked for it renders the project board exactly as upstream does.
// [delivery] ENABLE_SWIMLANES is read as a fallback for an app.ini written before the split.
func SwimlanesEnabled() bool {
	if setting.CfgProvider == nil {
		return false
	}
	return configKey(SettingSection, "ENABLE_SWIMLANES").MustBool(false)
}
