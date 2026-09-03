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

// withHubConfig installs an app.ini section for one test and puts the previous
// provider back, so a flag flipped here cannot leak into another test's page.
func withHubConfig(t *testing.T, ini string) {
	t.Helper()
	previous := setting.CfgProvider
	t.Cleanup(func() { setting.CfgProvider = previous })
	cfg, err := setting.NewConfigProviderFromData(ini)
	require.NoError(t, err)
	setting.CfgProvider = cfg
}

// TestPlanningReleasePageBadgesTheEnvironmentsHoldingARelease: the release page says
// where a release is running. The badges are drawn from the matrix endpoint, so the page
// introduces no second answer to what is live where.
func TestPlanningReleasePageBadgesTheEnvironmentsHoldingARelease(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	req := NewRequest(t, "GET", "/user2/repo1/releases")
	body := session.MakeRequest(t, req, http.StatusOK).Body.String()

	assert.Contains(t, body, "data-deployments-release-environments",
		"the release page carries the fork's one delegation")
	assert.Contains(t, body, `data-repo-id="1"`, "which names the repository the matrix is asked about")
	assert.Contains(t, body, "/api/deployments/v1", "and reads the matrix over the documented endpoint")

	// Signed out, the page is Gitea's alone: the matrix is not readable, so nothing is offered.
	req = NewRequest(t, "GET", "/user2/repo1/releases")
	anonymous := MakeRequest(t, req, http.StatusOK).Body.String()
	assert.NotContains(t, anonymous, "data-deployments-release-environments")
}

// TestPlanningSwimlanesAreGatedOnTheFlag: the project page is Gitea's, so
// a build that has not asked for lanes renders it carrying nothing of the fork's.
func TestPlanningSwimlanesAreGatedOnTheFlag(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user2")

	withHubConfig(t, "[planning]\nENABLE_SWIMLANES = false\n")
	req := NewRequest(t, "GET", "/user2/repo1/projects/1")
	body := session.MakeRequest(t, req, http.StatusOK).Body.String()
	assert.Contains(t, body, "project-board", "the upstream board still renders")
	assert.NotContains(t, body, "planning-swimlanes",
		"with the flag off the page carries nothing of the fork's")

	withHubConfig(t, "[planning]\nENABLE_SWIMLANES = true\n")
	req = NewRequest(t, "GET", "/user2/repo1/projects/1")
	body = session.MakeRequest(t, req, http.StatusOK).Body.String()
	assert.Contains(t, body, "planning-swimlane-grouping", "with it on the grouping selector renders")
	assert.Contains(t, body, `data-repo-id="1"`, "and names the repository the board API is asked about")
	assert.Contains(t, body, `data-project-id="1"`)
	for _, grouping := range []string{"type", "epic", "assignee"} {
		assert.Contains(t, body, `value="`+grouping+`"`, "the selector offers the grouping %q", grouping)
	}
}
