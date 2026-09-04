// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	deployments_model "gitea.dev/models/deployments"
	git_model "gitea.dev/models/git"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/commitstatus"
	"gitea.dev/modules/git"
	deploymentsv1 "gitea.dev/routers/api/deployments/v1"
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
	RepoFullName           string   `json:"repo_full_name"`
	Environment            string   `json:"environment"`
	ReleaseTag             string   `json:"release_tag"`
	CurrentlyLive          string   `json:"currently_live"`
	IsRollback             bool     `json:"is_rollback"`
	DependsOn              []string `json:"depends_on"`
	PredecessorState       string   `json:"predecessor_state"`
	Outcome                string   `json:"outcome"`
	Message                string   `json:"message"`
	SuggestedAction        string   `json:"suggested_action"`
	RequiresOverrideReason bool     `json:"requires_override_reason"`
	Confirmed              bool     `json:"confirmed"`
	WorkflowID             string   `json:"workflow_id"`
	Ref                    string   `json:"ref"`
}

// setEnvironmentPolicy writes one repository-scoped environment with the sequence policy the
// case needs. It goes through ValidateEnvironment, so a fixture the API would refuse to
// persist cannot be smuggled into a test.
func setEnvironmentPolicy(t *testing.T, env *deployments_model.Environment) {
	t.Helper()
	env.ReviewPolicy = deployments_model.PolicyNone
	env.RequiredReviewers = 1
	require.NoError(t, deployments_model.ValidateEnvironment(env))
	require.NoError(t, db.Insert(t.Context(), env))
}

// promote posts one deploy request and returns the status and the decoded payload.
func promote(t *testing.T, token string, body map[string]any) (int, promotionPayload) {
	t.Helper()
	req := NewRequestWithJSON(t, "POST", deploymentsv1.BasePath+"/deployments", body).AddTokenAuth(token)
	resp := MakeRequest(t, req, NoExpectedStatus)
	var payload promotionPayload
	DecodeJSON(t, resp, &payload)
	return resp.Code, payload
}

func hubWriteToken(t *testing.T, user string) string {
	t.Helper()
	return getTokenForLoggedInUser(t, loginUser(t, user),
		auth_model.AccessTokenScopeWriteRepository, auth_model.AccessTokenScopeReadRepository)
}

func hubAuditEvents(t *testing.T, event string) []*deployments_model.AuditEvent {
	t.Helper()
	rows, err := deployments_model.FindAuditEvents(t.Context(), builder.Eq{"event": event}, "id ASC", 0)
	require.NoError(t, err)
	return rows
}

// TestAPIDeploymentsPromotionPlanNamesWhatIsLiveBeforeDispatching is the first step over the
// wire: with confirm absent the response names the target environment, the release tag and
// the release currently live there, and nothing is dispatched. The log is counted as well as
// the response, because a response claiming "not dispatched" while dispatching would pass an
// assertion over the response alone.
func TestAPIDeploymentsPromotionPlanNamesWhatIsLiveBeforeDispatching(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	deployThroughTheNotifier(t, repo, sender, "prod", promotionPrerelease, 9101, 1000)

	setEnvironmentPolicy(t, &deployments_model.Environment{RepoID: repo.ID, Name: "prod", SortOrder: 50})
	before := len(hubAuditEvents(t, deployments_model.AuditRequested))

	status, plan := promote(t, hubWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease,
	})

	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, repo.FullName(), plan.RepoFullName)
	assert.Equal(t, "prod", plan.Environment)
	assert.Equal(t, promotionFullRelease, plan.ReleaseTag)
	assert.Equal(t, promotionPrerelease, plan.CurrentlyLive, "the confirm step names what it is replacing")
	assert.Equal(t, "deploy-prod.yaml", plan.WorkflowID)
	assert.Equal(t, "refs/tags/"+promotionFullRelease, plan.Ref)
	assert.False(t, plan.Confirmed)
	assert.Equal(t, "proceed", plan.Outcome, "no dependency is declared, so there is no sequence to warn about")

	assert.Len(t, hubAuditEvents(t, deployments_model.AuditRequested), before,
		"the first step appends nothing and dispatches nothing")
}

// TestAPIDeploymentsPromotionWarnsWhenTheDependencyNeverHeldIt: with require_prior_deployment
// off the sequence is a warning and the deploy is still offered, so an environment that has
// set no policy keeps the behaviour it had before the fork.
func TestAPIDeploymentsPromotionWarnsWhenTheDependencyNeverHeldIt(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	setEnvironmentPolicy(t, &deployments_model.Environment{RepoID: repo.ID, Name: "prod", SortOrder: 50, DependsOn: []string{"staging"}})

	status, plan := promote(t, hubWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease,
	})
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "warn", plan.Outcome)
	assert.Equal(t, "never", plan.PredecessorState)
	assert.Equal(t, []string{"staging"}, plan.DependsOn)
	assert.NotEmpty(t, plan.Message)
	assert.NotEmpty(t, plan.SuggestedAction, "every decision carries a suggested next action")
	assert.False(t, plan.RequiresOverrideReason, "a warning owes no reason")
}

// TestAPIDeploymentsPromotionProceedsOnceTheDependencyHasHeldIt is the gate's accepting case:
// the same environment, the same flag, and a dependency that has held the release.
func TestAPIDeploymentsPromotionProceedsOnceTheDependencyHasHeldIt(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	deployThroughTheNotifier(t, repo, sender, "staging", promotionFullRelease, 9201, 1000)

	setEnvironmentPolicy(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50,
		DependsOn: []string{"staging"}, RequirePriorDeployment: true,
	})

	status, plan := promote(t, hubWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease,
	})
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "proceed", plan.Outcome)
	assert.Equal(t, "live", plan.PredecessorState)
	assert.False(t, plan.RequiresOverrideReason)
}

// TestAPIDeploymentsPromotionAdminsCanBypassFalseRefusesTheAdmin is the refusing half: with
// admins_can_bypass unset, the repository admin is refused, and nothing is written.
func TestAPIDeploymentsPromotionAdminsCanBypassFalseRefusesTheAdmin(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	setEnvironmentPolicy(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50,
		DependsOn: []string{"staging"}, RequirePriorDeployment: true, AdminsCanBypass: false,
	})

	status, plan := promote(t, hubWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod",
		"release_tag": promotionFullRelease, "confirm": true,
		"override_reason": "I am an admin",
	})
	require.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "refuse", plan.Outcome)
	assert.False(t, plan.Confirmed)
	assert.Contains(t, plan.SuggestedAction, "bypass allowlist")
	assert.Empty(t, hubAuditEvents(t, deployments_model.AuditOverridden),
		"a refused deploy overrides nothing, whatever reason it sent")
}

// TestAPIDeploymentsPromotionOverrideLandsOnTheAuditLog is the accepting half: the override
// and its reason ARE an audit event.
//
// The dispatch that follows fails, because repo 1's tag carries no deploy-prod.yaml, and
// that is exactly the case worth pinning: the override was granted, and it is on the log
// whether or not the deploy it authorized then started.
func TestAPIDeploymentsPromotionOverrideLandsOnTheAuditLog(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	setEnvironmentPolicy(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50,
		DependsOn: []string{"staging"}, RequirePriorDeployment: true, AdminsCanBypass: true,
	})
	token := hubWriteToken(t, "user2") // user2 owns repo 1, so it is a repository admin

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
	require.Empty(t, hubAuditEvents(t, deployments_model.AuditOverridden), "no reason, no override")

	// With a reason, the override is recorded before anything else happens.
	req := NewRequestWithJSON(t, "POST", deploymentsv1.BasePath+"/deployments", map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease,
		"confirm": true, "override_reason": "hotfix; staging is down",
	}).AddTokenAuth(token)
	MakeRequest(t, req, NoExpectedStatus)

	overrides := hubAuditEvents(t, deployments_model.AuditOverridden)
	require.Len(t, overrides, 1)
	assert.Equal(t, "hotfix; staging is down", overrides[0].Reason)
	assert.Equal(t, "user2", overrides[0].ActorLogin, "the log names the human who bypassed the gate")
	assert.Equal(t, "prod", overrides[0].Environment)
	assert.Equal(t, promotionFullRelease, overrides[0].ReleaseTag)
	assert.Equal(t, deployments_model.SourceUI, overrides[0].Source)
}

// TestAPIDeploymentsPromotionChecksFailedLeavesNoOverrideAudit: an override request whose
// checks then fail is refused at 422, and the override it would have recorded never lands —
// the checks verdict is what gates writing the audit row, not merely being offered the
// override at the plan step.
func TestAPIDeploymentsPromotionChecksFailedLeavesNoOverrideAudit(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	setEnvironmentPolicy(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50,
		DependsOn: []string{"staging"}, RequirePriorDeployment: true, AdminsCanBypass: true,
		RequiredStatusContexts: []string{"ci/build"},
	})
	sha, err := git.NewIDFromString("65f1bf27bc3bf70f64657658635e66094edbcb4d")
	require.NoError(t, err)
	require.NoError(t, git_model.NewCommitStatus(t.Context(), git_model.NewCommitStatusOptions{
		Repo: repo, Creator: user2, SHA: sha,
		CommitStatus: &git_model.CommitStatus{State: commitstatus.CommitStatusFailure, Context: "ci/build"},
	}))
	token := hubWriteToken(t, "user2") // user2 owns repo 1, so it is a repository admin

	status, plan := promote(t, token, map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease,
	})
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "override", plan.Outcome, "the sequence rule alone would offer an override")
	assert.True(t, plan.RequiresOverrideReason)

	req := NewRequestWithJSON(t, "POST", deploymentsv1.BasePath+"/deployments", map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease,
		"confirm": true, "override_reason": "hotfix; staging is down",
	}).AddTokenAuth(token)
	resp := MakeRequest(t, req, NoExpectedStatus)
	require.Equal(t, http.StatusUnprocessableEntity, resp.Code, "the failing required status context refuses the deploy despite the override")

	unittest.AssertCount(t, &deployments_model.AuditEvent{
		RepoID: repo.ID, Environment: "prod", ReleaseTag: promotionFullRelease, Event: deployments_model.AuditOverridden,
	}, 0)
}

// TestAPIDeploymentsPromotionRefusesAPrereleaseWhereFullReleasesAreRequired covers the offer
// rule at the API, which is what makes it a rule rather than a hidden button: the CLI is
// refused where the grid is. The two environments differ only in releases_only, so
// nothing here turns on what either is called.
func TestAPIDeploymentsPromotionRefusesAPrereleaseWhereFullReleasesAreRequired(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	setEnvironmentPolicy(t, &deployments_model.Environment{RepoID: repo.ID, Name: "live", SortOrder: 50, ReleasesOnly: true})
	setEnvironmentPolicy(t, &deployments_model.Environment{RepoID: repo.ID, Name: "sandbox", SortOrder: 20})
	token := hubWriteToken(t, "user2")

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

// TestAPIDeploymentsPromotionIsRefusedWithoutWriteOnActions: authorization is
// Gitea's own check applied in process, and the API grants nothing the UI does not.
func TestAPIDeploymentsPromotionIsRefusedWithoutWriteOnActions(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	setEnvironmentPolicy(t, &deployments_model.Environment{RepoID: repo.ID, Name: "prod", SortOrder: 50})

	req := NewRequestWithJSON(t, "POST", deploymentsv1.BasePath+"/deployments", map[string]any{
		"repo": repo.FullName(), "environment": "prod",
		"release_tag": promotionFullRelease, "confirm": true,
	}).AddTokenAuth(hubWriteToken(t, "user4"))
	resp := MakeRequest(t, req, http.StatusForbidden)

	var err struct {
		Message         string `json:"message"`
		SuggestedAction string `json:"suggested_action"`
	}
	DecodeJSON(t, resp, &err)
	assert.NotEmpty(t, err.SuggestedAction, "every error carries a suggested next action")
}

// TestAPIDeploymentsPromotionNamesWhatItCannotFind covers the suggested next action on the request-shape paths.
func TestAPIDeploymentsPromotionNamesWhatItCannotFind(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	token := hubWriteToken(t, "user2")

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
			req := NewRequestWithJSON(t, "POST", deploymentsv1.BasePath+"/deployments", tc.body).AddTokenAuth(token)
			resp := MakeRequest(t, req, tc.status)
			var err struct {
				Message         string `json:"message"`
				SuggestedAction string `json:"suggested_action"`
			}
			DecodeJSON(t, resp, &err)
			assert.Contains(t, err.Message, tc.says)
			assert.NotEmpty(t, err.SuggestedAction, "every error carries a suggested next action")
		})
	}
}

// TestDeploymentsNewPageIsAClientOfTheAPI covers the confirm page: the handler
// ships the shell, and everything on the page arrives over POST /deployments.
func TestDeploymentsNewPageIsAClientOfTheAPI(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user2")
	resp := session.MakeRequest(t,
		NewRequest(t, "GET", "/deployments/new?repo=user2%2Frepo1&environment=prod&release_tag=v1.1"),
		http.StatusOK)
	body := resp.Body.String()
	// The page carries no script of its own: it mounts the bundled client and hands it the
	// namespace's own base through window.config.pageData. api.ts and NewPage.vue are what
	// name POST /deployments and its confirm field, proven in routers/web/hubroutes.
	assert.Contains(t, body, `"deploymentsNew":{"apiBase":"`+deploymentsv1.BasePath+`"`,
		"the page names the endpoint it is a client of")
	assert.Contains(t, body, `data-global-init="initDeploymentsNew"`,
		"the page mounts the bundled client that is the API's actual client")
}
