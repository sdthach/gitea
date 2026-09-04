// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
)

// TestPlanningSettingsRepoPage covers the repository-scoped settings page: a signed-out
// visitor is sent to sign in, a repository administrator gets the mount point with
// canWrite true, a reader with no admin on the repository gets it with canWrite false, and a
// private repository the doer cannot see is 404 rather than a hint that it exists.
func TestPlanningSettingsRepoPage(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/planning/settings/user2/repo1")
	MakeRequest(t, req, http.StatusSeeOther)

	session := loginUser(t, "user2")
	req = NewRequest(t, "GET", "/planning/settings/user2/repo1")
	resp := session.MakeRequest(t, req, http.StatusOK)
	body := resp.Body.String()
	assert.Contains(t, body, `data-global-init="initPlanningSettings"`)
	assert.Contains(t, body, "planningSettings")
	assert.Contains(t, body, `"canWrite":true`)

	// user2/repo1 is public, so user4 can read it but administers neither it nor its owner.
	reader := loginUser(t, "user4")
	req = NewRequest(t, "GET", "/planning/settings/user2/repo1")
	resp = reader.MakeRequest(t, req, http.StatusOK)
	assert.Contains(t, resp.Body.String(), `"canWrite":false`)

	// repo2 is private to user2 in the fixtures; user4 has no collaboration or team access to it.
	req = NewRequest(t, "GET", "/planning/settings/user2/repo2")
	reader.MakeRequest(t, req, http.StatusNotFound)
}
