// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	auth_model "gitea.dev/models/auth"
	hub_model "gitea.dev/models/hub"
	"gitea.dev/models/unittest"
	planningv1 "gitea.dev/routers/api/planning/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
)

// TestPlanningProjectPage covers the project page shell: a signed-out visitor is sent to
// sign in, a signed-in one with unit access on the repository gets the mount point, one
// without it gets a 404 rather than a hint that the repository exists, and rendering the
// page more than once reuses the same cached page token rather than minting a fresh one
// each time.
func TestPlanningProjectPage(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/planning/projects/user2/repo1/1")
	MakeRequest(t, req, http.StatusSeeOther)

	session := loginUser(t, "user2")
	req = NewRequest(t, "GET", "/planning/projects/user2/repo1/1")
	resp := session.MakeRequest(t, req, http.StatusOK)
	body := resp.Body.String()
	assert.Contains(t, body, `data-global-init="initPlanningProject"`)
	assert.Contains(t, body, "planningProject")
	assert.Contains(t, body, `"canWrite":true`)
	assert.Contains(t, body, `"canEditIssues":true`)

	// repo2 is private to user2 in the fixtures; user4 has no collaboration or team access
	// to it.
	outsider := loginUser(t, "user4")
	req = NewRequest(t, "GET", "/planning/projects/user2/repo2/1")
	outsider.MakeRequest(t, req, http.StatusNotFound)

	req = NewRequest(t, "GET", "/planning/projects/user2/repo1/1")
	session.MakeRequest(t, req, http.StatusOK)
	unittest.AssertCount(t, &auth_model.AccessToken{UID: 2, Name: hub_model.PageTokenName}, 1)

	// A cached token is per-session-owner: rendering the same page as a different user must
	// mint that user's own token rather than reusing user2's cached one.
	user4Session := loginUser(t, "user4")
	req = NewRequest(t, "GET", "/planning/projects/user2/repo1/1")
	resp = user4Session.MakeRequest(t, req, http.StatusOK)
	unittest.AssertCount(t, &auth_model.AccessToken{UID: 4, Name: hub_model.PageTokenName}, 1)
	unittest.AssertCount(t, &auth_model.AccessToken{UID: 2, Name: hub_model.PageTokenName}, 1)

	// user2/repo1 is public, so user4 can read it but holds no write on either unit: both
	// flags come back false, gating both the board's inline editors and the saved-views writer.
	readerBody := resp.Body.String()
	assert.Contains(t, readerBody, `"canWrite":false`)
	assert.Contains(t, readerBody, `"canEditIssues":false`)
}

// TestPlanningAPIAcceptsTheBrowserSessionForReads: a page's own JS calls the API with
// nothing but the browser session, so a GET must not be turned away for lacking a token.
// The same session posting a write still gets refused: only a minted token, not the
// session alone, carries write scope.
//
// This alone does not catch a regression to two independent session managers: tests/sqlite.ini
// sets [session] PROVIDER = file, and every request in this package shares that one on-disk
// store regardless of which router's common.MustInitSessioner() built it, so the read here
// passes whether or not the manager is actually shared. A memory-provider variant would catch
// that, but common.MustInitSessioner is a sync.OnceValue: the whole integration binary runs one
// server, earlier tests have already forced it to build its manager from this file's config
// before this test runs, and tests.PrepareTestEnv does not restart the router, so flipping
// setting.SessionConfig.Provider here would change a global the already-built manager never
// reads again. TestMustInitSessionerSharesOneManager in routers/common is the guard for this
// defect instead.
func TestPlanningAPIAcceptsTheBrowserSessionForReads(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")

	req := NewRequest(t, "GET", planningv1.BasePath+"/board?repo_id=1&project_id=1")
	session.MakeRequest(t, req, http.StatusOK)

	req = NewRequestWithJSON(t, "POST", planningv1.BasePath+"/issues/1/milestone",
		map[string]any{"repo": "user2/repo1", "milestone_id": 0})
	session.MakeRequest(t, req, http.StatusForbidden)
}
