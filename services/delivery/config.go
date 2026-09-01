// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package delivery holds the fork's service layer. It reads its own configuration section
// rather than adding a field to an upstream settings struct, which would be a second edit
// to an upstream file (F2).
package delivery

import "gitea.dev/modules/setting"

// SettingSection is the app.ini section the fork reads.
const SettingSection = "delivery"

// PagesEnabled reports whether the delivery pages are served. It mirrors
// reqMilestonesDashboardPageEnabled's shape, so the whole feature can be switched off with
// one app.ini key (F13):
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
