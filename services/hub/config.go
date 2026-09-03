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

// SettingSection is the app.ini section the fork reads. It is declared once, beside the
// model that also reads it.
const SettingSection = deployments_model.SettingSection

// PagesEnabled reports whether the delivery pages are served. It mirrors
// reqMilestonesDashboardPageEnabled's shape, so the whole feature can be switched off with
// one app.ini key:
//
//	[delivery]
//	ENABLE_PAGES = false
//
// It defaults to true, so a fork build behaves the same whether or not the section exists.
func PagesEnabled() bool {
	if setting.CfgProvider == nil {
		return true
	}
	return setting.CfgProvider.Section(SettingSection).Key("ENABLE_PAGES").MustBool(true)
}

// SwimlanesEnabled reports whether Gitea's own repository project page offers the fork's
// lane grouping:
//
//	[delivery]
//	ENABLE_SWIMLANES = true
//
// It defaults to FALSE, unlike the pages: this one changes a page the fork does not own, so
// a build that has not asked for it renders the project board exactly as upstream does.
func SwimlanesEnabled() bool {
	if setting.CfgProvider == nil {
		return false
	}
	return setting.CfgProvider.Section(SettingSection).Key("ENABLE_SWIMLANES").MustBool(false)
}
