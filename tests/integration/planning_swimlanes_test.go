// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"strings"
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

	// The fragment carries no script of its own: it mounts the fork's bundled client, once,
	// which is what draws a badge onto each release entry the matrix says is live somewhere.
	assert.Contains(t, body, `data-global-init="initDeploymentsReleaseBadges"`,
		"the release page mounts the fork's one delegation")
	assert.Equal(t, 1, strings.Count(body, `data-global-init="initDeploymentsReleaseBadges"`),
		"the fragment mounts once per page, not once per release entry")
	assert.Contains(t, body, `data-repo-id="1"`, "which names the repository the matrix is asked about")
	assert.Contains(t, body, `data-api-base="/api/deployments/v1"`, "and reads the matrix over the documented endpoint")

	// Signed out, the page is Gitea's alone: the matrix is not readable, so nothing is offered.
	req = NewRequest(t, "GET", "/user2/repo1/releases")
	anonymous := MakeRequest(t, req, http.StatusOK).Body.String()
	assert.NotContains(t, anonymous, `data-global-init="initDeploymentsReleaseBadges"`)
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
	assert.Contains(t, body, `href="/planning/projects/user2/repo1/1?view=board"`,
		"with it on the fragment links to the project page's board view")
	assert.Contains(t, body, "Open in Projects board")
}
