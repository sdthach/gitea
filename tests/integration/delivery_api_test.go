// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	"gitea.dev/models/delivery"
	"gitea.dev/modules/setting"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fork's integration tests run under Gitea's own harness, so they execute the way
// upstream's do and survive a rebase (J8).

// deliveryFixtureEnvironments is how many rows models/fixtures/delivery_environment.yml
// defines at repo_id 0. Nothing about the count or the names is a fork default; an operator
// names their own set in [delivery] DEFAULT_ENVIRONMENTS.
const deliveryFixtureEnvironments = 5

func TestAPIDeliveryEnvironmentsAreListed(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/environments").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)

	var envs []*delivery.Environment
	DecodeJSON(t, resp, &envs)
	require.Len(t, envs, deliveryFixtureEnvironments, "the instance-wide set is listed whatever it is called")

	names := make([]string, len(envs))
	for i, env := range envs {
		names[i] = env.Name
		assert.Equal(t, delivery.PolicyNone, env.ApprovalPolicy, "adding the fork gates nothing until a policy is set (F5b)")
	}
	assert.Equal(t, []string{"dev", "qa", "uat", "staging", "prod"}, names, "environments render in configured order (F9)")

	assert.NotEmpty(t, resp.Header().Get("X-Total-Count"), "offset-paged resources carry X-Total-Count (I7)")
}

func TestAPIDeliveryRejectsAnUnknownFilterField(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/environments?colour=red").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusBadRequest)

	var payload struct {
		Code            string   `json:"code"`
		Message         string   `json:"message"`
		SuggestedAction string   `json:"suggested_action"`
		Accepted        []string `json:"accepted"`
	}
	DecodeJSON(t, resp, &payload)
	assert.Equal(t, "unknown_filter_field", payload.Code)
	assert.Contains(t, payload.Message, "colour", "the rejection names the offender (I4)")
	assert.NotEmpty(t, payload.SuggestedAction, "every error carries a suggested next action (A21)")
	assert.NotEmpty(t, payload.Accepted, "the rejection lists what is accepted (I4)")
}

func TestAPIDeliveryAppliesFiltersSortAndPaging(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/environments?sort_order[gte]=40&sort_by=name&order=asc").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var filtered []*delivery.Environment
	DecodeJSON(t, resp, &filtered)
	require.Len(t, filtered, 2)
	assert.Equal(t, "prod", filtered[0].Name)
	assert.Equal(t, "staging", filtered[1].Name)

	req = NewRequest(t, "GET", deliveryv1.BasePath+"/environments?q=prod").AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusOK)
	var searched []*delivery.Environment
	DecodeJSON(t, resp, &searched)
	require.Len(t, searched, 1)
	assert.Equal(t, "prod", searched[0].Name)

	req = NewRequest(t, "GET", deliveryv1.BasePath+"/environments?limit=2&page=2").AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusOK)
	var paged []*delivery.Environment
	DecodeJSON(t, resp, &paged)
	require.Len(t, paged, 2)
	assert.Equal(t, "uat", paged[0].Name, "page 2 of the sort-order sequence starts at the third environment")
	assert.NotEmpty(t, resp.Header().Get("Link"), "offset paging carries an RFC 5988 Link header (I7)")
}

func TestAPIDeliveryRequiresSignIn(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/environments")
	resp := MakeRequest(t, req, http.StatusForbidden)
	assert.Contains(t, resp.Body.String(), "suggested_action")
}

// TestDeliveryPageIsBehindSignIn covers F13: each page sits behind reqSignIn and a settings
// gate, and the page itself is a client of the API rather than a second data path.
func TestDeliveryPageIsBehindSignIn(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/delivery/environments/prod")
	MakeRequest(t, req, http.StatusSeeOther)

	session := loginUser(t, "user2")
	req = NewRequest(t, "GET", "/delivery/environments/prod")
	resp := session.MakeRequest(t, req, http.StatusOK)
	body := resp.Body.String()
	assert.Contains(t, body, deliveryv1.BasePath+"/environments", "the page fetches its rows from the documented endpoint (E18, I14)")
}

// The tests below cover the repository-scoped endpoints and, with them, repoWithActions —
// the authorization helper that resolves permission through Gitea's own check on the
// Actions unit rather than a second model of permissions (E10, I13).

func TestAPIDeliveryRepoEnvironmentsFallBackToTheDefaultSet(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/repos/user2/repo1/environments").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)

	var envs []*delivery.Environment
	DecodeJSON(t, resp, &envs)
	require.Len(t, envs, deliveryFixtureEnvironments,
		"a repository that has declared no environment of its own renders the instance-wide default set")
	assert.Equal(t, delivery.DefaultsRepoID, envs[0].RepoID)
}

func TestAPIDeliveryGetRepoEnvironment(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/repos/user2/repo1/environments/PROD").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var env delivery.Environment
	DecodeJSON(t, resp, &env)
	assert.Equal(t, "prod", env.Name, "environment names are identifiers, matched case-insensitively")
	assert.Equal(t, delivery.PolicyNone, env.ApprovalPolicy)

	// An unknown name is a hub error rendered through renderHubError, which carries the
	// suggested next action A21 requires.
	req = NewRequest(t, "GET", deliveryv1.BasePath+"/repos/user2/repo1/environments/nowhere").AddTokenAuth(token)
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

func TestAPIDeliveryRepos(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/repos?q=repo1&sort_by=id&order=asc").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)

	var repos []*deliveryv1.Repository
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

// TestAPIDeliverySecretNamesNeverCarryAValue is I12 over the wire.
func TestAPIDeliverySecretNamesNeverCarryAValue(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	require.NoError(t, db.Insert(t.Context(), &delivery.SecretScope{RepoID: 1, SecretName: "PROD_DB_PASS", Environment: "prod"}))
	require.NoError(t, db.Insert(t.Context(), &delivery.SecretScope{RepoID: 1, SecretName: "QA_DB_PASS", Environment: "qa"}))

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/repos/user2/repo1/environments/prod/secrets").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)

	var names []map[string]any
	DecodeJSON(t, resp, &names)
	require.Len(t, names, 1, "only the scope rows of the requested environment are listed")
	assert.Equal(t, "PROD_DB_PASS", names[0]["name"])
	assert.Equal(t, "prod", names[0]["environment"])
	assert.Equal(t, true, names[0]["scoped"])
	assert.Equal(t, false, names[0]["exists"], "no secret of that name is configured in this repository yet")
	for _, forbidden := range []string{"value", "data", "secret", "plaintext"} {
		assert.NotContains(t, names[0], forbidden, "a secret value is never readable over any endpoint at any scope (I12)")
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
	req := NewRequest(t, "GET", deliveryv1.BasePath+"/repos/user2/repo1/environments").AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)

	// user4 has no write on its Actions unit, so secret metadata is refused.
	req = NewRequest(t, "GET", deliveryv1.BasePath+"/repos/user2/repo1/environments/prod/secrets").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusForbidden)
	var refusal struct {
		Code            string `json:"code"`
		Message         string `json:"message"`
		SuggestedAction string `json:"suggested_action"`
	}
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "forbidden", refusal.Code)
	assert.Contains(t, refusal.Message, "write")
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action (A21)")

	// A repository the caller cannot see is a 404, not a 403 that would confirm it exists.
	req = NewRequest(t, "GET", deliveryv1.BasePath+"/repos/user2/no-such-repo/environments").AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusNotFound)
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "repo_not_found", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction)
}

// TestDeliveryPagesCanBeSwitchedOff is F13's settings gate: the whole feature can be turned
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
	req = NewRequest(t, "GET", deliveryv1.BasePath+"/environments").AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
}
