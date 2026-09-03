// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"testing"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	deployments_model "gitea.dev/models/deployments"
	repo_model "gitea.dev/models/repo"
	unit_model "gitea.dev/models/unit"
	"gitea.dev/modules/setting"
	deploymentsv1 "gitea.dev/routers/api/deployments/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fork's integration tests run under Gitea's own harness, so they execute the way
// upstream's do and survive a rebase.

// deliveryFixtureEnvironments is how many rows models/fixtures/deploy_environment.yml
// defines at repo_id 0. Nothing about the count or the names is a fork default; an operator
// names their own set in [delivery] DEFAULT_ENVIRONMENTS.
const deliveryFixtureEnvironments = 5

func TestAPIDeliveryEnvironmentsAreListed(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", deploymentsv1.BasePath+"/environments").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)

	var envs []*deployments_model.Environment
	DecodeJSON(t, resp, &envs)
	require.Len(t, envs, deliveryFixtureEnvironments, "the instance-wide set is listed whatever it is called")

	names := make([]string, len(envs))
	for i, env := range envs {
		names[i] = env.Name
		assert.Equal(t, deployments_model.PolicyNone, env.ReviewPolicy, "adding the fork gates nothing until a policy is set")
	}
	assert.Equal(t, []string{"dev", "qa", "uat", "staging", "prod"}, names, "environments render in configured order")

	assert.NotEmpty(t, resp.Header().Get("X-Total-Count"), "offset-paged resources carry X-Total-Count")
}

func TestAPIDeliveryRejectsAnUnknownFilterField(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", deploymentsv1.BasePath+"/environments?colour=red").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusBadRequest)

	var payload struct {
		Code            string   `json:"code"`
		Message         string   `json:"message"`
		SuggestedAction string   `json:"suggested_action"`
		Accepted        []string `json:"accepted"`
	}
	DecodeJSON(t, resp, &payload)
	assert.Equal(t, "unknown_filter_field", payload.Code)
	assert.Contains(t, payload.Message, "colour", "the rejection names the offender")
	assert.NotEmpty(t, payload.SuggestedAction, "every error carries a suggested next action")
	assert.NotEmpty(t, payload.Accepted, "the rejection lists what is accepted")
}

func TestAPIDeliveryAppliesFiltersSortAndPaging(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", deploymentsv1.BasePath+"/environments?sort_order[gte]=40&sort_by=name&order=asc").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var filtered []*deployments_model.Environment
	DecodeJSON(t, resp, &filtered)
	require.Len(t, filtered, 2)
	assert.Equal(t, "prod", filtered[0].Name)
	assert.Equal(t, "staging", filtered[1].Name)

	req = NewRequest(t, "GET", deploymentsv1.BasePath+"/environments?q=prod").AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusOK)
	var searched []*deployments_model.Environment
	DecodeJSON(t, resp, &searched)
	require.Len(t, searched, 1)
	assert.Equal(t, "prod", searched[0].Name)

	req = NewRequest(t, "GET", deploymentsv1.BasePath+"/environments?limit=2&page=2").AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusOK)
	var paged []*deployments_model.Environment
	DecodeJSON(t, resp, &paged)
	require.Len(t, paged, 2)
	assert.Equal(t, "uat", paged[0].Name, "page 2 of the sort-order sequence starts at the third environment")
	assert.NotEmpty(t, resp.Header().Get("Link"), "offset paging carries an RFC 5988 Link header")
}

func TestAPIDeliveryRequiresSignIn(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", deploymentsv1.BasePath+"/environments")
	resp := MakeRequest(t, req, http.StatusForbidden)
	assert.Contains(t, resp.Body.String(), "suggested_action")
}

// TestDeliveryPageIsBehindSignIn: each page sits behind reqSignIn and a settings
// gate, and the page itself is a client of the API rather than a second data path.
func TestDeliveryPageIsBehindSignIn(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/delivery/environments/prod")
	MakeRequest(t, req, http.StatusSeeOther)

	session := loginUser(t, "user2")
	req = NewRequest(t, "GET", "/delivery/environments/prod")
	resp := session.MakeRequest(t, req, http.StatusOK)
	body := resp.Body.String()
	assert.Contains(t, body, deploymentsv1.BasePath+"/environments", "the page fetches its rows from the documented endpoint")
}

// TestDeliveryEnvironmentPagesAreClientsOfTheAPI covers the editor's two screens: the list
// and the per-row detail, both behind reqSignIn, both reading the row over the API.
func TestDeliveryEnvironmentPagesAreClientsOfTheAPI(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	for _, path := range []string{"/delivery/environments", "/delivery/environments/1/edit"} {
		MakeRequest(t, NewRequest(t, "GET", path), http.StatusSeeOther)
	}

	session := loginUser(t, "user2")

	resp := session.MakeRequest(t, NewRequest(t, "GET", "/delivery/environments"), http.StatusOK)
	body := resp.Body.String()
	assert.Contains(t, body, deploymentsv1.BasePath+"/environments", "the list is a client of the documented endpoint")
	assert.Contains(t, body, `const envID = "";`, "the list screen names no single row")

	resp = session.MakeRequest(t, NewRequest(t, "GET", "/delivery/environments/1/edit"), http.StatusOK)
	body = resp.Body.String()
	assert.Contains(t, body, `const envID = "1";`, "the detail screen reads the row the path names, not one resolved by name")
	assert.Contains(t, body, "Danger zone")
}

// The tests below cover the repository-scoped endpoints and, with them, repoWithActions —
// the authorization helper that resolves permission through Gitea's own check on the
// Actions unit rather than a second model of permissions.

func TestAPIDeliveryRepoEnvironmentsFallBackToTheDefaultSet(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", deploymentsv1.BasePath+"/repos/user2/repo1/environments").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)

	var envs []*deployments_model.Environment
	DecodeJSON(t, resp, &envs)
	require.Len(t, envs, deliveryFixtureEnvironments,
		"a repository that has declared no environment of its own renders the instance-wide default set")
	assert.Equal(t, deployments_model.DefaultsRepoID, envs[0].RepoID)
}

func TestAPIDeliveryGetRepoEnvironment(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", deploymentsv1.BasePath+"/repos/user2/repo1/environments/PROD").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var env deployments_model.Environment
	DecodeJSON(t, resp, &env)
	assert.Equal(t, "prod", env.Name, "environment names are identifiers, matched case-insensitively")
	assert.Equal(t, deployments_model.PolicyNone, env.ReviewPolicy)

	// An unknown name is a hub error rendered through renderHubError, which carries a
	// suggested next action.
	req = NewRequest(t, "GET", deploymentsv1.BasePath+"/repos/user2/repo1/environments/nowhere").AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusNotFound)
	var failure struct {
		Code            string `json:"code"`
		Message         string `json:"message"`
		SuggestedAction string `json:"suggested_action"`
	}
	DecodeJSON(t, resp, &failure)
	assert.Contains(t, failure.Message, "nowhere")
	assert.NotEmpty(t, failure.SuggestedAction)
}

type deliveryEnvironmentRow struct {
	ID              int64  `json:"id"`
	RepoID          int64  `json:"repo_id"`
	Name            string `json:"name"`
	AdminsCanBypass bool   `json:"admins_can_bypass"`
	CanWrite        bool   `json:"can_write"`
}

func deliveryEnvironmentToken(t *testing.T, login string) string {
	t.Helper()
	return getTokenForLoggedInUser(t, loginUser(t, login), auth_model.AccessTokenScopeReadRepository)
}

func TestAPIDeliveryEnvironmentCanWrite(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// user5 owns repo4, so it is repository administrator there; user4 is a collaborator
	// with write on it, which the gate refuses.
	repoEnv := &deployments_model.Environment{
		RepoID: 4, Name: "prod", SortOrder: 50,
		ReviewPolicy: deployments_model.PolicyNone, RequiredReviewers: 1,
	}
	require.NoError(t, db.Insert(t.Context(), repoEnv))

	canWrite := func(login string, envID int64) bool {
		t.Helper()
		req := NewRequest(t, "GET", deploymentsv1.BasePath+"/environments?limit=50").
			AddTokenAuth(deliveryEnvironmentToken(t, login))
		var rows []deliveryEnvironmentRow
		DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &rows)
		for _, row := range rows {
			if row.ID == envID {
				return row.CanWrite
			}
		}
		t.Fatalf("%s cannot see environment %d at all, so the row says nothing about can_write", login, envID)
		return false
	}

	const defaultsEnvID = 1 // models/fixtures/deploy_environment.yml, repo_id 0
	assert.False(t, canWrite("user2", defaultsEnvID), "an ordinary user may not write the instance-wide default set")
	assert.True(t, canWrite("user1", defaultsEnvID), "a site administrator may")
	assert.True(t, canWrite("user5", repoEnv.ID), "the repository's administrator may write its own environment")
	assert.False(t, canWrite("user4", repoEnv.ID), "write on a repository is not admin on it")
}

// TestAPIDeliveryEnvironmentByID covers GET /environments/{id}: identity is the id, and a row
// the caller cannot see is refused as one that does not exist.
func TestAPIDeliveryEnvironmentByID(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// user2/repo2 is private and carries no Actions unit; adding one leaves visibility as
	// the only thing separating it from the rows user4 can read.
	require.NoError(t, db.Insert(t.Context(), &repo_model.RepoUnit{
		RepoID: 2, Type: unit_model.TypeActions, Config: &repo_model.ActionsConfig{},
	}))
	envs := map[int64]*deployments_model.Environment{}
	for _, repoID := range []int64{1, 2, 4} {
		env := &deployments_model.Environment{
			RepoID: repoID, Name: "prod", SortOrder: 50,
			ReviewPolicy: deployments_model.PolicyNone, RequiredReviewers: 1,
		}
		require.NoError(t, db.Insert(t.Context(), env))
		envs[repoID] = env
	}

	read := func(login string, id int64, status int) deliveryEnvironmentRow {
		t.Helper()
		req := NewRequest(t, "GET", fmt.Sprintf("%s/environments/%d", deploymentsv1.BasePath, id)).
			AddTokenAuth(deliveryEnvironmentToken(t, login))
		var row deliveryEnvironmentRow
		resp := MakeRequest(t, req, status)
		if status == http.StatusOK {
			DecodeJSON(t, resp, &row)
		}
		return row
	}

	// Three repositories name an environment "prod"; only the id tells them apart.
	assert.Equal(t, int64(1), read("user4", envs[1].ID, http.StatusOK).RepoID)
	repo4Row := read("user4", envs[4].ID, http.StatusOK)
	assert.Equal(t, int64(4), repo4Row.RepoID)
	assert.Equal(t, "prod", repo4Row.Name)
	assert.False(t, repo4Row.CanWrite, "user4 has write on repo4, not admin")
	assert.True(t, read("user5", envs[4].ID, http.StatusOK).CanWrite, "its owner may write it")

	// A write answers with the same row shape a read does, can_write included.
	writeToken := getTokenForLoggedInUser(t, loginUser(t, "user5"), auth_model.AccessTokenScopeAll)
	body := map[string]any{"repo_id": 4, "name": "qa", "sort_order": 20, "review_policy": "none", "required_reviewers": 1}
	var written deliveryEnvironmentRow
	req := NewRequestWithJSON(t, "POST", deploymentsv1.BasePath+"/environments", body).AddTokenAuth(writeToken)
	DecodeJSON(t, MakeRequest(t, req, http.StatusCreated), &written)
	assert.True(t, written.CanWrite)
	assert.True(t, written.AdminsCanBypass, "a create body without admins_can_bypass defaults to true")
	body["sort_order"] = 30
	req = NewRequestWithJSON(t, "PUT", fmt.Sprintf("%s/environments/%d", deploymentsv1.BasePath, written.ID), body).AddTokenAuth(writeToken)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &written)
	assert.True(t, written.CanWrite)

	falseBody := map[string]any{"repo_id": 4, "name": "qa-no-bypass", "review_policy": "none", "required_reviewers": 1, "admins_can_bypass": false}
	var writtenFalse deliveryEnvironmentRow
	req = NewRequestWithJSON(t, "POST", deploymentsv1.BasePath+"/environments", falseBody).AddTokenAuth(writeToken)
	DecodeJSON(t, MakeRequest(t, req, http.StatusCreated), &writtenFalse)
	assert.False(t, writtenFalse.AdminsCanBypass, "a create body with admins_can_bypass: false is honored")

	// A row in a repository the caller cannot see is answered exactly as one that does not
	// exist, so the 404 never confirms the row is there. Its owner still reads it.
	assert.Equal(t, int64(2), read("user2", envs[2].ID, http.StatusOK).RepoID)
	hidden := deliveryEnvironmentRefusal(t, "user4", envs[2].ID)
	missing := deliveryEnvironmentRefusal(t, "user4", 999999)
	assert.Equal(t, missing.Code, hidden.Code)
	assert.NotEmpty(t, missing.SuggestedAction, "every error carries a suggested next action")
}

func deliveryEnvironmentRefusal(t *testing.T, login string, id int64) deliveryRefusal {
	t.Helper()
	var refusal deliveryRefusal
	req := NewRequest(t, "GET", fmt.Sprintf("%s/environments/%d", deploymentsv1.BasePath, id)).
		AddTokenAuth(deliveryEnvironmentToken(t, login))
	DecodeJSON(t, MakeRequest(t, req, http.StatusNotFound), &refusal)
	return refusal
}

func TestAPIDeliveryRepos(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", deploymentsv1.BasePath+"/repos?q=repo1&sort_by=id&order=asc").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)

	var repos []*deploymentsv1.Repository
	DecodeJSON(t, resp, &repos)
	require.NotEmpty(t, repos)
	found := false
	for _, r := range repos {
		assert.NotEmpty(t, r.FullName)
		if r.FullName == "user2/repo1" {
			found = true
		}
	}
	assert.True(t, found, "the caller's own repository is listed")
	assert.NotEmpty(t, resp.Header().Get("X-Total-Count"))
}

// TestAPIDeliverySecretNamesNeverCarryAValue asserts over the wire that a secret value is
// never readable at any scope.
func TestAPIDeliverySecretNamesNeverCarryAValue(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	require.NoError(t, db.Insert(t.Context(), &deployments_model.SecretScope{RepoID: 1, SecretName: "PROD_DB_PASS", Environment: "prod"}))
	require.NoError(t, db.Insert(t.Context(), &deployments_model.SecretScope{RepoID: 1, SecretName: "QA_DB_PASS", Environment: "qa"}))

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	req := NewRequest(t, "GET", deploymentsv1.BasePath+"/repos/user2/repo1/environments/prod/secrets").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)

	var names []map[string]any
	DecodeJSON(t, resp, &names)
	require.Len(t, names, 1, "only the scope rows of the requested environment are listed")
	assert.Equal(t, "PROD_DB_PASS", names[0]["name"])
	assert.Equal(t, "prod", names[0]["environment"])
	assert.Equal(t, true, names[0]["scoped"])
	assert.Equal(t, false, names[0]["exists"], "no secret of that name is configured in this repository yet")
	for _, forbidden := range []string{"value", "data", "secret", "plaintext"} {
		assert.NotContains(t, names[0], forbidden, "a secret value is never readable over any endpoint at any scope")
	}
	assert.NotContains(t, resp.Body.String(), "prod-value")
}

// TestAPIDeliveryAuthorizesThroughGiteasOwnCheck exercises both branches of repoWithActions:
// read access is enough to list environments, write on the Actions unit is required for
// secret metadata, and an unknown repository is a 404 with a next action.
func TestAPIDeliveryAuthorizesThroughGiteasOwnCheck(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user4")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository, auth_model.AccessTokenScopeWriteRepository)

	// user4 can read user2/repo1, which is public with the Actions unit enabled.
	req := NewRequest(t, "GET", deploymentsv1.BasePath+"/repos/user2/repo1/environments").AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)

	// user4 has no write on its Actions unit, so secret metadata is refused.
	req = NewRequest(t, "GET", deploymentsv1.BasePath+"/repos/user2/repo1/environments/prod/secrets").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusForbidden)
	var refusal struct {
		Code            string `json:"code"`
		Message         string `json:"message"`
		SuggestedAction string `json:"suggested_action"`
	}
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "forbidden", refusal.Code)
	assert.Contains(t, refusal.Message, "write")
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")

	// A repository the caller cannot see is a 404, not a 403 that would confirm it exists.
	req = NewRequest(t, "GET", deploymentsv1.BasePath+"/repos/user2/no-such-repo/environments").AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusNotFound)
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "repo_not_found", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)
}

// TestDeliveryPagesCanBeSwitchedOff covers the settings gate: the whole feature can be turned
// off with one app.ini key, mirroring reqMilestonesDashboardPageEnabled.
func TestDeliveryPagesCanBeSwitchedOff(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	req := NewRequest(t, "GET", "/delivery/environments/prod")
	session.MakeRequest(t, req, http.StatusOK)

	previous := setting.CfgProvider
	t.Cleanup(func() { setting.CfgProvider = previous })
	provider, err := setting.NewConfigProviderFromData("[delivery]\nENABLE_PAGES = false")
	require.NoError(t, err)
	setting.CfgProvider = provider

	req = NewRequest(t, "GET", "/delivery/environments/prod")
	session.MakeRequest(t, req, http.StatusForbidden)

	// The API namespace is unaffected: the gate is on the pages, not on the contract.
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)
	req = NewRequest(t, "GET", deploymentsv1.BasePath+"/environments").AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
}
