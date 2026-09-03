// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"testing"

	"gitea.dev/modules/setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPagesEnabled(t *testing.T) {
	previous := setting.CfgProvider
	t.Cleanup(func() { setting.CfgProvider = previous })

	setting.CfgProvider = nil
	assert.True(t, PagesEnabled(), "with no configuration at all the fork behaves as a stock build plus the pages")

	cases := map[string]bool{
		"":                                 true,
		"[delivery]\n":                     true,
		"[delivery]\nENABLE_PAGES = true":  true,
		"[delivery]\nENABLE_PAGES = false": false,
	}
	for ini, want := range cases {
		t.Run(ini, func(t *testing.T) {
			provider, err := setting.NewConfigProviderFromData(ini)
			require.NoError(t, err)
			setting.CfgProvider = provider
			assert.Equal(t, want, PagesEnabled())
		})
	}
}
