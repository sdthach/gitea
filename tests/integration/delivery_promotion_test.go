// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	"gitea.dev/models/delivery"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xorm.io/builder"
)

// Repository 1 carries the Actions unit and two releases the fixtures already declare:
// v1.1 is a full release and v1.0 is a prerelease. user2 owns it, so user2 is its
// repository admin; user4 has no access to it at all.
const (
	promotionFullRelease = "v1.1"
	promotionPrerelease  = "v1.0"
)

// promotionPayload is the shape POST /deployments answers with. It is declared here rather
// than imported so the test asserts the WIRE contract, not the Go struct: a field renamed in
// the service without the document following would still pass an assertion over the struct.
type promotionPayload struct {
	RepoFullName           string `json:"repo_full_name"`
	Environment            string `json:"environment"`
	ReleaseTag             string `json:"release_tag"`
	CurrentlyLive          string `json:"currently_live"`
	IsRollback             bool   `json:"is_rollback"`
	Predecessor            string `json:"predecessor"`
	PredecessorState       string `json:"predecessor_state"`
	Outcome                string `json:"outcome"`
	Message                string `json:"message"`
	SuggestedAction        string `json:"suggested_action"`
	RequiresOverrideReason bool   `json:"requires_override_reason"`
	Confirmed              bool   `json:"confirmed"`
	WorkflowID             string `json:"workflow_id"`
	Ref                    string `json:"ref"`
}

// setEnvironmentPolicy writes one repository-scoped environment with the sequence policy the
// case needs. It goes through ValidateEnvironment, so a fixture the API would refuse to
// persist cannot be smuggled into a test.
func setEnvironmentPolicy(t *testing.T, env *delivery.Environment) {
	t.Helper()
	env.ApprovalPolicy = delivery.PolicyNone
	env.RequiredApprovals = 1
	require.NoError(t, delivery.ValidateEnvironment(env))
	require.NoError(t, db.Insert(t.Context(), env))
}

// promote posts one deploy request and returns the status and the decoded payload.
func promote(t *testing.T, token string, body map[string]any) (int, promotionPayload) {
	t.Helper()
	req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/deployments", body).AddTokenAuth(token)
	resp := MakeRequest(t, req, NoExpectedStatus)
	var payload promotionPayload
	DecodeJSON(t, resp, &payload)
	return resp.Code, payload
}

func deliveryWriteToken(t *testing.T, user string) string {
	t.Helper()
	return getTokenForLoggedInUser(t, loginUser(t, user),
		auth_model.AccessTokenScopeWriteRepository, auth_model.AccessTokenScopeReadRepository)
}

func deliveryAuditEvents(t *testing.T, event string) []*delivery.AuditEvent {
	t.Helper()
	rows, err := delivery.FindAuditEvents(t.Context(), builder.Eq{"event": event}, "id ASC", 0)
	require.NoError(t, err)
	return rows
}

// TestAPIDeliveryPromotionPlanNamesWhatIsLiveBeforeDispatching is E14's first step over the
// wire: with confirm absent the response names the target environment, the release tag and
// the release currently live there, and nothing is dispatched. The log is counted as well as
// the response, because a response claiming "not dispatched" while dispatching would pass an
// assertion over the response alone.
func TestAPIDeliveryPromotionPlanNamesWhatIsLiveBeforeDispatching(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	deployThroughTheNotifier(t, repo, sender, "prod", promotionPrerelease, 9101, 1000)

	setEnvironmentPolicy(t, &delivery.Environment{RepoID: repo.ID, Name: "prod", SortOrder: 50})
	before := len(deliveryAuditEvents(t, delivery.AuditRequested))

	status, plan := promote(t, deliveryWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease,
	})

	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, repo.FullName(), plan.RepoFullName)
	assert.Equal(t, "prod", plan.Environment)
	assert.Equal(t, promotionFullRelease, plan.ReleaseTag)
	assert.Equal(t, promotionPrerelease, plan.CurrentlyLive, "the confirm step names what it is replacing (E14)")
	assert.Equal(t, "deploy-prod.yaml", plan.WorkflowID)
	assert.Equal(t, "refs/tags/"+promotionFullRelease, plan.Ref)
	assert.False(t, plan.Confirmed)
	assert.Equal(t, "proceed", plan.Outcome, "no predecessor is declared, so there is no sequence to warn about")

	assert.Len(t, deliveryAuditEvents(t, delivery.AuditRequested), before,
		"the first step appends nothing and dispatches nothing (E14)")
}

// TestAPIDeliveryPromotionWarnsWhenThePredecessorNeverHeldIt is F11: with
// require_predecessor off the sequence is a warning and the deploy is still offered, so an
// environment that has set no policy behaves as it did before this slice.
func TestAPIDeliveryPromotionWarnsWhenThePredecessorNeverHeldIt(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	setEnvironmentPolicy(t, &delivery.Environment{RepoID: repo.ID, Name: "prod", SortOrder: 50, Predecessor: "staging"})

	status, plan := promote(t, deliveryWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease,
	})
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "warn", plan.Outcome)
	assert.Equal(t, "never", plan.PredecessorState)
	assert.Equal(t, "staging", plan.Predecessor)
	assert.NotEmpty(t, plan.Message)
	assert.NotEmpty(t, plan.SuggestedAction, "every decision carries a suggested next action (A21)")
	assert.False(t, plan.RequiresOverrideReason, "a warning owes no reason")
}

// TestAPIDeliveryPromotionProceedsOnceThePredecessorHasHeldIt is the gate's accepting case:
// the same environment, the same flag, and a predecessor that has held the release.
func TestAPIDeliveryPromotionProceedsOnceThePredecessorHasHeldIt(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	deployThroughTheNotifier(t, repo, sender, "staging", promotionFullRelease, 9201, 1000)

	setEnvironmentPolicy(t, &delivery.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50,
		Predecessor: "staging", RequirePredecessor: true,
	})

	status, plan := promote(t, deliveryWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease,
	})
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "proceed", plan.Outcome)
	assert.Equal(t, "live", plan.PredecessorState)
	assert.False(t, plan.RequiresOverrideReason)
}

// TestAPIDeliveryPromotionBlockAdminOverrideRefusesTheAdmin is SC 40's refusing half: with
// block_admin_override set, the repository admin is refused, and nothing is written.
func TestAPIDeliveryPromotionBlockAdminOverrideRefusesTheAdmin(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	setEnvironmentPolicy(t, &delivery.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50,
		Predecessor: "staging", RequirePredecessor: true, BlockAdminOverride: true,
	})

	status, plan := promote(t, deliveryWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod",
		"release_tag": promotionFullRelease, "confirm": true,
		"override_reason": "I am an admin",
	})
	require.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "refuse", plan.Outcome)
	assert.False(t, plan.Confirmed)
	assert.Contains(t, plan.SuggestedAction, "bypass allowlist")
	assert.Empty(t, deliveryAuditEvents(t, delivery.AuditOverridden),
		"a refused deploy overrides nothing, whatever reason it sent")
}

// TestAPIDeliveryPromotionOverrideLandsOnTheAuditLog is SC 40's accepting half and E17's
// requirement that the override and its reason ARE an audit event.
//
// The dispatch that follows fails, because repo 1's tag carries no deploy-prod.yaml, and
// that is exactly the case worth pinning: the override was granted, and it is on the log
// whether or not the deploy it authorized then started.
func TestAPIDeliveryPromotionOverrideLandsOnTheAuditLog(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	setEnvironmentPolicy(t, &delivery.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50,
		Predecessor: "staging", RequirePredecessor: true,
	})
	token := deliveryWriteToken(t, "user2") // user2 owns repo 1, so it is a repository admin

	// Step one: the admin is offered the override and told a reason is owed.
	status, plan := promote(t, token, map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease,
	})
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "override", plan.Outcome)
	assert.True(t, plan.RequiresOverrideReason)

	// Confirming without one is refused, and writes nothing.
	status, plan = promote(t, token, map[string]any{
		"repo": repo.FullName(), "environment": "prod",
		"release_tag": promotionFullRelease, "confirm": true,
	})
	require.Equal(t, http.StatusBadRequest, status)
	assert.False(t, plan.Confirmed)
	require.Empty(t, deliveryAuditEvents(t, delivery.AuditOverridden), "no reason, no override")

	// With a reason, the override is recorded before anything else happens.
	req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/deployments", map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease,
		"confirm": true, "override_reason": "hotfix; staging is down",
	}).AddTokenAuth(token)
	MakeRequest(t, req, NoExpectedStatus)

	overrides := deliveryAuditEvents(t, delivery.AuditOverridden)
	require.Len(t, overrides, 1)
	assert.Equal(t, "hotfix; staging is down", overrides[0].Reason)
	assert.Equal(t, "user2", overrides[0].ActorLogin, "the log names the human who bypassed the gate")
	assert.Equal(t, "prod", overrides[0].Environment)
	assert.Equal(t, promotionFullRelease, overrides[0].ReleaseTag)
	assert.Equal(t, delivery.SourceUI, overrides[0].Source)
}

// TestAPIDeliveryPromotionRefusesAPrereleaseWhereFullReleasesAreRequired covers the offer
// rule at the API, which is what makes it a rule rather than a hidden button: the CLI is
// refused where the grid is. The two environments differ only in require_full_release, so
// nothing here turns on what either is called.
func TestAPIDeliveryPromotionRefusesAPrereleaseWhereFullReleasesAreRequired(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	setEnvironmentPolicy(t, &delivery.Environment{RepoID: repo.ID, Name: "live", SortOrder: 50, RequireFullRelease: true})
	setEnvironmentPolicy(t, &delivery.Environment{RepoID: repo.ID, Name: "sandbox", SortOrder: 20})
	token := deliveryWriteToken(t, "user2")

	status, plan := promote(t, token, map[string]any{
		"repo": repo.FullName(), "environment": "live", "release_tag": promotionPrerelease,
	})
	require.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "refuse", plan.Outcome)
	assert.Contains(t, plan.Message, "prerelease")
	assert.NotEmpty(t, plan.SuggestedAction)

	status, plan = promote(t, token, map[string]any{
		"repo": repo.FullName(), "environment": "sandbox", "release_tag": promotionPrerelease,
	})
	require.Equal(t, http.StatusOK, status, "an environment that has not asked for full releases takes a prerelease")
	assert.Equal(t, "proceed", plan.Outcome)
}

// TestAPIDeliveryPromotionIsRefusedWithoutWriteOnActions is E10 and K6: authorization is
// Gitea's own check applied in process, and the API grants nothing the UI does not.
func TestAPIDeliveryPromotionIsRefusedWithoutWriteOnActions(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	setEnvironmentPolicy(t, &delivery.Environment{RepoID: repo.ID, Name: "prod", SortOrder: 50})

	req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/deployments", map[string]any{
		"repo": repo.FullName(), "environment": "prod",
		"release_tag": promotionFullRelease, "confirm": true,
	}).AddTokenAuth(deliveryWriteToken(t, "user4"))
	resp := MakeRequest(t, req, http.StatusForbidden)

	var err struct {
		Message         string `json:"message"`
		SuggestedAction string `json:"suggested_action"`
	}
	DecodeJSON(t, resp, &err)
	assert.NotEmpty(t, err.SuggestedAction, "every error carries a suggested next action (A21)")
}

// TestAPIDeliveryPromotionNamesWhatItCannotFind covers A21 on the request-shape paths.
func TestAPIDeliveryPromotionNamesWhatItCannotFind(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	token := deliveryWriteToken(t, "user2")

	for _, tc := range []struct {
		name   string
		body   map[string]any
		status int
		says   string
	}{
		{
			"unknown environment",
			map[string]any{"repo": repo.FullName(), "environment": "moon", "release_tag": promotionFullRelease},
			http.StatusNotFound, "moon",
		},
		{
			"unknown release",
			map[string]any{"repo": repo.FullName(), "environment": "prod", "release_tag": "v99"},
			http.StatusNotFound, "v99",
		},
		{
			"repo that is not owner/name",
			map[string]any{"repo": "web", "environment": "prod", "release_tag": promotionFullRelease},
			http.StatusBadRequest, "owner/name",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/deployments", tc.body).AddTokenAuth(token)
			resp := MakeRequest(t, req, tc.status)
			var err struct {
				Message         string `json:"message"`
				SuggestedAction string `json:"suggested_action"`
			}
			DecodeJSON(t, resp, &err)
			assert.Contains(t, err.Message, tc.says)
			assert.NotEmpty(t, err.SuggestedAction, "every error carries a suggested next action (A21)")
		})
	}
}

// TestDeliveryPromotePageIsAClientOfTheAPI is E18/I14 for the confirm page: the handler
// ships the shell, and everything on the page arrives over POST /deployments.
func TestDeliveryPromotePageIsAClientOfTheAPI(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	resp := session.MakeRequest(t,
		NewRequest(t, "GET", "/delivery/promote?repo=user2%2Frepo1&environment=prod&release_tag=v1.1"),
		http.StatusOK)
	body := resp.Body.String()
	assert.Contains(t, body, deliveryv1.BasePath+"/deployments",
		"the page names the endpoint it is a client of (E18, I14)")
	assert.Contains(t, body, "confirm")
}
