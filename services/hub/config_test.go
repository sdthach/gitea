// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"testing"

	"gitea.dev/modules/setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanningPagesEnabled(t *testing.T) {
	previous := setting.CfgProvider
	t.Cleanup(func() { setting.CfgProvider = previous })

	setting.CfgProvider = nil
	assert.True(t, PlanningPagesEnabled(), "with no configuration at all the fork behaves as a stock build plus the pages")

	cases := map[string]bool{
		"":                                 true,
		"[delivery]\n":                     true,
		"[delivery]\nENABLE_PAGES = true":  true,
		"[delivery]\nENABLE_PAGES = false": false,
		"[planning]\n":                     true,
		"[planning]\nENABLE_PAGES = true":  true,
		"[planning]\nENABLE_PAGES = false": false,
		// the new section wins when both are set.
		"[delivery]\nENABLE_PAGES = false\n[planning]\nENABLE_PAGES = true": true,
	}
	for ini, want := range cases {
		t.Run(ini, func(t *testing.T) {
			provider, err := setting.NewConfigProviderFromData(ini)
			require.NoError(t, err)
			setting.CfgProvider = provider
			assert.Equal(t, want, PlanningPagesEnabled())
		})
	}
}

func TestDeploymentsPagesEnabled(t *testing.T) {
	previous := setting.CfgProvider
	t.Cleanup(func() { setting.CfgProvider = previous })

	setting.CfgProvider = nil
	assert.True(t, DeploymentsPagesEnabled(), "with no configuration at all the fork behaves as a stock build plus the pages")

	cases := map[string]bool{
		"":                                    true,
		"[delivery]\n":                        true,
		"[delivery]\nENABLE_PAGES = true":     true,
		"[delivery]\nENABLE_PAGES = false":    false,
		"[deployments]\n":                     true,
		"[deployments]\nENABLE_PAGES = true":  true,
		"[deployments]\nENABLE_PAGES = false": false,
		// the new section wins when both are set.
		"[delivery]\nENABLE_PAGES = false\n[deployments]\nENABLE_PAGES = true": true,
	}
	for ini, want := range cases {
		t.Run(ini, func(t *testing.T) {
			provider, err := setting.NewConfigProviderFromData(ini)
			require.NoError(t, err)
			setting.CfgProvider = provider
			assert.Equal(t, want, DeploymentsPagesEnabled())
		})
	}
}

func TestSwimlanesEnabled(t *testing.T) {
	previous := setting.CfgProvider
	t.Cleanup(func() { setting.CfgProvider = previous })

	setting.CfgProvider = nil
	assert.False(t, SwimlanesEnabled(), "unlike the pages, this changes a page the fork does not own, so it defaults off")

	cases := map[string]bool{
		"":                                       false,
		"[delivery]\n":                           false,
		"[delivery]\nENABLE_SWIMLANES = true":    true,
		"[deployments]\nENABLE_SWIMLANES = true": false, // swimlanes decorate the board, a planning page, not deployments
		"[planning]\n":                           false,
		"[planning]\nENABLE_SWIMLANES = true":    true,
		// the new section wins when both are set.
		"[delivery]\nENABLE_SWIMLANES = true\n[planning]\nENABLE_SWIMLANES = false": false,
	}
	for ini, want := range cases {
		t.Run(ini, func(t *testing.T) {
			provider, err := setting.NewConfigProviderFromData(ini)
			require.NoError(t, err)
			setting.CfgProvider = provider
			assert.Equal(t, want, SwimlanesEnabled())
		})
	}
}
