// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	"gitea.dev/modules/setting"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withDeliveryConfig installs a [delivery] section for one test and puts the previous
// provider back, so a flag flipped here cannot leak into another test's page.
func withDeliveryConfig(t *testing.T, ini string) {
	t.Helper()
	previous := setting.CfgProvider
	t.Cleanup(func() { setting.CfgProvider = previous })
	cfg, err := setting.NewConfigProviderFromData(ini)
	require.NoError(t, err)
	setting.CfgProvider = cfg
}

// TestDeliverySwimlanesAreGatedOnTheFlag is D2's constraint: the project page is Gitea's, so
// a build that has not asked for lanes renders it carrying nothing of the fork's.
func TestDeliverySwimlanesAreGatedOnTheFlag(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user2")

	withDeliveryConfig(t, "[delivery]\nENABLE_SWIMLANES = false\n")
	req := NewRequest(t, "GET", "/user2/repo1/projects/1")
	body := session.MakeRequest(t, req, http.StatusOK).Body.String()
	assert.Contains(t, body, "project-board", "the upstream board still renders")
	assert.NotContains(t, body, "delivery-swimlanes",
		"with the flag off the page carries nothing of the fork's")

	withDeliveryConfig(t, "[delivery]\nENABLE_SWIMLANES = true\n")
	req = NewRequest(t, "GET", "/user2/repo1/projects/1")
	body = session.MakeRequest(t, req, http.StatusOK).Body.String()
	assert.Contains(t, body, "delivery-swimlane-grouping", "with it on the grouping selector renders")
	assert.Contains(t, body, `data-repo-id="1"`, "and names the repository the board API is asked about")
	assert.Contains(t, body, `data-project-id="1"`)
	for _, grouping := range []string{"type", "epic", "assignee"} {
		assert.Contains(t, body, `value="`+grouping+`"`, "the selector offers the grouping %q", grouping)
	}
}
