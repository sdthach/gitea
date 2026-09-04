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

// TestPlanningSettingsOwnerPage covers the owner-scoped settings page: an organization owner
// gets canWrite true and the organization's own title, a member who is not an owner gets
// canWrite false, and a plain user's own page is the instance scope — writable only by a site
// administrator, titled accordingly rather than after the user who happens to be looking at it.
func TestPlanningSettingsOwnerPage(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// user2 owns org3 in the fixtures (team_user.yml team_id 1, the Owners team).
	owner := loginUser(t, "user2")
	req := NewRequest(t, "GET", "/planning/settings/org3")
	resp := owner.MakeRequest(t, req, http.StatusOK)
	assert.Contains(t, resp.Body.String(), `"canWrite":true`)

	// user4 belongs to org3 (team_user.yml team_id 2, write) but does not own it.
	member := loginUser(t, "user4")
	req = NewRequest(t, "GET", "/planning/settings/org3")
	resp = member.MakeRequest(t, req, http.StatusOK)
	assert.Contains(t, resp.Body.String(), `"canWrite":false`)

	// user2 is not an organization: /planning/settings/user2 edits the instance scope, so only
	// a site administrator may write there, and the page is titled for that scope, not for user2.
	admin := loginUser(t, "user1")
	req = NewRequest(t, "GET", "/planning/settings/user2")
	resp = admin.MakeRequest(t, req, http.StatusOK)
	assert.Contains(t, resp.Body.String(), `"canWrite":true`)
	assert.Contains(t, resp.Body.String(), `"orgId":0`)
	assert.Contains(t, resp.Body.String(), "<title>Instance planning settings")

	req = NewRequest(t, "GET", "/planning/settings/user2")
	resp = owner.MakeRequest(t, req, http.StatusOK)
	assert.Contains(t, resp.Body.String(), `"canWrite":false`)
}
