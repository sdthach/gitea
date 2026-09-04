// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	actions_model "gitea.dev/models/actions"
	"gitea.dev/models/db"
	deployments_model "gitea.dev/models/deployments"
	git_model "gitea.dev/models/git"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/commitstatus"
	"gitea.dev/modules/git"
	"gitea.dev/modules/timeutil"
	deploymentsv1 "gitea.dev/routers/api/deployments/v1"
	deployments_service "gitea.dev/services/deployments"
	"gitea.dev/services/notify"
	release_service "gitea.dev/services/release"
	repo_service "gitea.dev/services/repository"
	files_service "gitea.dev/services/repository/files"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xorm.io/builder"
)

// checksPayload is what POST /deployments and GET /deployments/{id}/checks answer with, as
// the wire spells it — asserted independently of the Go struct so a rename there without the
// document following would fail here too.
type checksPayload struct {
	RepoFullName string         `json:"repo_full_name"`
	Environment  string         `json:"environment"`
	ReleaseTag   string         `json:"release_tag"`
	SHA          string         `json:"sha"`
	Outcome      string         `json:"outcome"`
	State        string         `json:"state"`
	Confirmed    bool           `json:"confirmed"`
	DeploymentID int64          `json:"deployment_id"`
	Checks       []checkPayload `json:"checks"`
}

type checkPayload struct {
	Name            string `json:"name"`
	State           string `json:"state"`
	Reason          string `json:"reason"`
	SuggestedAction string `json:"suggested_action"`
	RetryAt         int64  `json:"retry_at"`
}

func checkNamed(checks []checkPayload, name string) (checkPayload, bool) {
	for _, c := range checks {
		if c.Name == name {
			return c, true
		}
	}
	return checkPayload{}, false
}

// promoteChecks posts one deploy request and decodes it into checksPayload.
func promoteChecks(t *testing.T, token string, body map[string]any) (int, checksPayload) {
	t.Helper()
	req := NewRequestWithJSON(t, "POST", deploymentsv1.BasePath+"/deployments", body).AddTokenAuth(token)
	resp := MakeRequest(t, req, NoExpectedStatus)
	var payload checksPayload
	DecodeJSON(t, resp, &payload)
	return resp.Code, payload
}

// writeChecksEnvironment inserts one repository-scoped environment carrying the checks
// fields a case needs, going through ValidateEnvironment so a fixture the API would refuse
// cannot be smuggled in.
func writeChecksEnvironment(t *testing.T, env *deployments_model.Environment) {
	t.Helper()
	if env.ReviewPolicy == "" {
		env.ReviewPolicy = deployments_model.PolicyNone
	}
	if env.RequiredReviewers == 0 {
		env.RequiredReviewers = 1
	}
	require.NoError(t, deployments_model.ValidateEnvironment(env))
	require.NoError(t, db.Insert(t.Context(), env))
}

func hubDeploymentsFor(t *testing.T, repoID int64, environment, releaseTag string) []*deployments_model.Deployment {
	t.Helper()
	rows, err := deployments_model.FindDeployments(t.Context(),
		builder.Eq{"repo_id": repoID, "environment": environment, "release_tag": releaseTag}, "id ASC", 0)
	require.NoError(t, err)
	return rows
}

// recordRunEvent drives the fork's own notifier directly, the same capture point every
// deploy reaches, leaving a deployment row at status without requiring a real Actions run.
func recordRunEvent(t *testing.T, repo *repo_model.Repository, sender *user_model.User, environment, tag string, runID int64, status actions_model.Status) {
	t.Helper()
	require.NoError(t, repo.LoadOwner(t.Context()))
	run := &actions_model.ActionRun{
		ID: runID, RepoID: repo.ID, WorkflowID: "deploy-" + environment + ".yaml",
		Ref: "refs/tags/" + tag, CommitSHA: "65f1bf27bc3bf70f64657658635e66094edbcb4d",
		Status: status, Updated: timeutil.TimeStampNow(),
	}
	notify.WorkflowRunStatusUpdate(t.Context(), repo, sender, run)
}

// seedDeployableRelease creates a fresh repository carrying a real deploy-<environment>.yaml
// workflow_dispatch workflow and a release tag pointing at it, so a real Promote dispatch has
// an actual workflow to find — repo 1's own fixture tags carry none, which is what every
// other deploy in this file relies on to test a checks-only outcome without a real dispatch.
func seedDeployableRelease(t *testing.T, owner *user_model.User, environment string) (*repo_model.Repository, string) {
	t.Helper()
	repo, err := repo_service.CreateRepository(t.Context(), owner, owner, repo_service.CreateRepoOptions{
		Name:     fmt.Sprintf("deploy-seed-%s-%d", environment, time.Now().UnixNano()),
		AutoInit: true, DefaultBranch: "main", Readme: "Default",
	})
	require.NoError(t, err)

	content := "on: workflow_dispatch\njobs:\n  deploy:\n    runs-on: ubuntu-latest\n    steps:\n      - run: ./deploy.sh\n"
	_, err = files_service.ChangeRepoFiles(t.Context(), repo, owner, &files_service.ChangeRepoFilesOptions{
		Files: []*files_service.ChangeRepoFile{
			{Operation: "create", TreePath: ".gitea/workflows/deploy-" + environment + ".yaml", ContentReader: strings.NewReader(content)},
		},
		Message: "add deploy workflow", OldBranch: "main", NewBranch: "main",
		Author:    &files_service.IdentityOptions{GitUserName: owner.Name, GitUserEmail: owner.Email},
		Committer: &files_service.IdentityOptions{GitUserName: owner.Name, GitUserEmail: owner.Email},
		Dates:     &files_service.CommitDateOptions{Author: time.Now(), Committer: time.Now()},
	})
	require.NoError(t, err)

	gitRepo, err := git.OpenRepository(t.Context(), repo)
	require.NoError(t, err)
	defer gitRepo.Close()

	const tag = "v1.0.0"
	require.NoError(t, release_service.CreateRelease(t.Context(), gitRepo, &repo_model.Release{
		RepoID: repo.ID, Repo: repo, PublisherID: owner.ID, Publisher: owner,
		TagName: tag, Target: "main", Title: tag, IsTag: true,
	}, nil, ""))
	return repo, tag
}

// TestDeploymentsChecksRequiredStatusContextMissingWaitsThenReevaluateDispatches: a required
// context that has never reported on the release commit may still turn green, so it holds the
// deploy waiting rather than failing it outright; once it reports success, ReevaluateWaiting
// dispatches.
func TestDeploymentsChecksRequiredStatusContextMissingWaitsThenReevaluateDispatches(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	writeChecksEnvironment(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50,
		RequiredStatusContexts: []string{"ci/build"},
	})
	token := hubWriteToken(t, "user2")

	status, payload := promoteChecks(t, token, map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease, "confirm": true,
	})

	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "waiting", payload.State)
	check, ok := checkNamed(payload.Checks, "required_status_contexts")
	require.True(t, ok)
	assert.Equal(t, "wait", check.State)
	assert.Contains(t, check.Reason, "ci/build")
	require.NotZero(t, check.RetryAt)

	sha, err := git.NewIDFromString("65f1bf27bc3bf70f64657658635e66094edbcb4d")
	require.NoError(t, err)
	require.NoError(t, git_model.NewCommitStatus(t.Context(), git_model.NewCommitStatusOptions{
		Repo: repo, Creator: user2, SHA: sha,
		CommitStatus: &git_model.CommitStatus{State: commitstatus.CommitStatusSuccess, Context: "ci/build"},
	}))
	require.NoError(t, deployments_service.ReevaluateWaiting(t.Context(), timeutil.TimeStampNow().AsTime().Unix()))
	assert.NotEmpty(t, hubAuditEvents(t, deployments_model.AuditChecksPassed), "the context reported success; the gate opened")
}

// TestDeploymentsChecksRequiredStatusContextFailureFailsTheDeploy: a context that HAS
// reported, and reported failure, fails the deploy outright rather than waiting — a report is
// terminal, unlike silence.
func TestDeploymentsChecksRequiredStatusContextFailureFailsTheDeploy(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	writeChecksEnvironment(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50,
		RequiredStatusContexts: []string{"ci/build"},
	})
	sha, err := git.NewIDFromString("65f1bf27bc3bf70f64657658635e66094edbcb4d")
	require.NoError(t, err)
	require.NoError(t, git_model.NewCommitStatus(t.Context(), git_model.NewCommitStatusOptions{
		Repo: repo, Creator: user2, SHA: sha,
		CommitStatus: &git_model.CommitStatus{State: commitstatus.CommitStatusFailure, Context: "ci/build"},
	}))
	before := len(hubAuditEvents(t, deployments_model.AuditChecksFailed))

	status, payload := promoteChecks(t, hubWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease, "confirm": true,
	})

	require.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "checks_failed", payload.State)
	check, ok := checkNamed(payload.Checks, "required_status_contexts")
	require.True(t, ok)
	assert.Equal(t, "fail", check.State)
	assert.Contains(t, check.Reason, "ci/build")

	assert.Empty(t, hubDeploymentsFor(t, repo.ID, "prod", promotionFullRelease), "nothing dispatched or written")
	assert.Len(t, hubAuditEvents(t, deployments_model.AuditChecksFailed), before,
		"checks_failed at the API step, before anything is written, appends nothing itself")
}

// TestDeploymentsChecksRequiredStatusContextSuccessPasses is the accepting half: once the
// context reports success the same check passes.
func TestDeploymentsChecksRequiredStatusContextSuccessPasses(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	writeChecksEnvironment(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50,
		RequiredStatusContexts: []string{"ci/build"},
	})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	sha, err := git.NewIDFromString("65f1bf27bc3bf70f64657658635e66094edbcb4d")
	require.NoError(t, err)
	require.NoError(t, git_model.NewCommitStatus(t.Context(), git_model.NewCommitStatusOptions{
		Repo: repo, Creator: user2, SHA: sha,
		CommitStatus: &git_model.CommitStatus{State: commitstatus.CommitStatusSuccess, Context: "ci/build"},
	}))

	// The plan step (confirm omitted) already carries checks; reading it here does not chance
	// on repo1's own deploy-prod.yaml existing, which the dispatch itself would need.
	_, payload := promoteChecks(t, hubWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease,
	})
	check, ok := checkNamed(payload.Checks, "required_status_contexts")
	require.True(t, ok)
	assert.Equal(t, "pass", check.State)
}

// TestDeploymentsChecksWaitTimerHoldsThenReevaluateDispatches: a wait timer creates a waiting
// deployment; ReevaluateWaiting leaves it waiting before retry_at and dispatches it once now
// passes retry_at — proven by the checks_passed audit event, which only ever fires on the
// gate opening, never on the gate itself.
func TestDeploymentsChecksWaitTimerHoldsThenReevaluateDispatches(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	writeChecksEnvironment(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50, WaitMinutes: 10,
	})

	status, payload := promoteChecks(t, hubWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease, "confirm": true,
	})
	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "waiting", payload.State)
	require.NotZero(t, payload.DeploymentID)
	check, ok := checkNamed(payload.Checks, "wait_timer")
	require.True(t, ok)
	assert.Equal(t, "wait", check.State)
	require.NotZero(t, check.RetryAt)

	rows := hubDeploymentsFor(t, repo.ID, "prod", promotionFullRelease)
	require.Len(t, rows, 1)
	requested := int64(rows[0].CreatedUnix)

	require.NoError(t, deployments_service.ReevaluateWaiting(t.Context(), requested+1))
	assert.Empty(t, hubAuditEvents(t, deployments_model.AuditChecksPassed), "the timer has not elapsed yet")
	assert.Len(t, hubDeploymentsFor(t, repo.ID, "prod", promotionFullRelease), 1, "still exactly the one waiting row")

	require.NoError(t, deployments_service.ReevaluateWaiting(t.Context(), check.RetryAt+1))
	assert.NotEmpty(t, hubAuditEvents(t, deployments_model.AuditChecksPassed), "the timer has elapsed; the gate opened")
}

// TestDeploymentsChecksClosedWindowHoldsWithNextOpening: a window that excludes every day of
// the week is always closed, and its retry_at is exactly NextOpening's own answer.
func TestDeploymentsChecksClosedWindowHoldsWithNextOpening(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	window := &deployments_model.DeployWindow{DaysMask: 0b100_0000, FromMinute: 0, ToMinute: 1, Timezone: "UTC"} // Saturday only, one minute
	writeChecksEnvironment(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50, DeployWindow: window,
	})

	_, payload := promoteChecks(t, hubWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease, "confirm": true,
	})
	check, ok := checkNamed(payload.Checks, "deployment_window")
	require.True(t, ok)
	if check.State == "pass" {
		t.Skip("the test ran during the one open minute of the week; not worth pinning to a wider window")
	}
	assert.Equal(t, "wait", check.State)
	want, err := deployments_service.NextOpening(timeutil.TimeStampNow().AsTime().Unix(), window)
	require.NoError(t, err)
	assert.InDelta(t, want, check.RetryAt, 60, "retry_at is NextOpening's own answer, give or take the request's own second")
}

// TestDeploymentsChecksExclusiveLockHoldsASecondDeploy: a second deploy to an
// exclusive_lock environment already running one deploy waits rather than dispatching.
func TestDeploymentsChecksExclusiveLockHoldsASecondDeploy(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	writeChecksEnvironment(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50, ExclusiveLock: true,
	})
	recordRunEvent(t, repo, sender, "prod", promotionFullRelease, 9301, actions_model.StatusRunning)

	status, payload := promoteChecks(t, hubWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionPrerelease, "confirm": true,
	})
	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "waiting", payload.State)
	check, ok := checkNamed(payload.Checks, "exclusive_lock")
	require.True(t, ok)
	assert.Equal(t, "wait", check.State)

	running := hubDeploymentsFor(t, repo.ID, "prod", promotionFullRelease)
	require.Len(t, running, 1)
	assert.Equal(t, "running", running[0].Status, "the first deployment is untouched")

	// Once the first run reports success, exclusiveLockCheck no longer counts it as busy,
	// and the second — excluded from counting its OWN waiting row against itself — clears
	// the lock and dispatches on the next sweep.
	recordRunEvent(t, repo, sender, "prod", promotionFullRelease, 9301, actions_model.StatusSuccess)
	require.NoError(t, deployments_service.ReevaluateWaiting(t.Context(), timeutil.TimeStampNow().AsTime().Unix()))
	assert.NotEmpty(t, hubAuditEvents(t, deployments_model.AuditChecksPassed), "the first run finished; the lock cleared")
}

// TestDeploymentsChecksExclusiveLockDoesNotCrossEnvironments: a running deployment on one
// environment must not hold the lock for a different environment of the same repository —
// exclusive_lock is scoped per environment, not per repository.
func TestDeploymentsChecksExclusiveLockDoesNotCrossEnvironments(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	writeChecksEnvironment(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "staging", SortOrder: 40, ExclusiveLock: true,
	})
	writeChecksEnvironment(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50, ExclusiveLock: true,
	})
	recordRunEvent(t, repo, sender, "staging", promotionFullRelease, 9801, actions_model.StatusRunning)

	// The plan step (confirm omitted) already carries checks, without needing repo1's own
	// deploy-prod.yaml workflow, which an actual dispatch would.
	status, payload := promoteChecks(t, hubWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionFullRelease,
	})
	require.Equal(t, http.StatusOK, status)
	check, ok := checkNamed(payload.Checks, "exclusive_lock")
	require.True(t, ok)
	assert.Equal(t, "pass", check.State, "staging's own running deployment must not hold prod's lock")
}

// TestDeploymentsChecksExclusiveLockAndWaitTimerBothClearTogether: exclusive_lock and a wait
// timer stacked on the same environment both have to clear before the held deploy dispatches
// — clearing the lock alone is not enough while the timer has not elapsed, and the timer
// elapsing alone is not enough while the lock is still held.
func TestDeploymentsChecksExclusiveLockAndWaitTimerBothClearTogether(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	writeChecksEnvironment(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "prod", SortOrder: 50, ExclusiveLock: true, WaitMinutes: 10,
	})
	recordRunEvent(t, repo, sender, "prod", promotionFullRelease, 9302, actions_model.StatusRunning)

	status, payload := promoteChecks(t, hubWriteToken(t, "user2"), map[string]any{
		"repo": repo.FullName(), "environment": "prod", "release_tag": promotionPrerelease, "confirm": true,
	})
	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "waiting", payload.State)
	lock, ok := checkNamed(payload.Checks, "exclusive_lock")
	require.True(t, ok)
	assert.Equal(t, "wait", lock.State, "the lock holds regardless of the timer")
	timer, ok := checkNamed(payload.Checks, "wait_timer")
	require.True(t, ok)
	assert.Equal(t, "wait", timer.State, "the timer holds regardless of the lock")
	rows := hubDeploymentsFor(t, repo.ID, "prod", promotionPrerelease)
	require.Len(t, rows, 1)
	requested := int64(rows[0].CreatedUnix)

	// The timer has elapsed but the first deploy is still running: the lock alone still
	// holds it.
	require.NoError(t, deployments_service.ReevaluateWaiting(t.Context(), requested+11*60))
	assert.Empty(t, hubAuditEvents(t, deployments_model.AuditChecksPassed), "the lock is still held")

	// The first run finishes: both gates are now open, and the second dispatches.
	recordRunEvent(t, repo, sender, "prod", promotionFullRelease, 9302, actions_model.StatusSuccess)
	require.NoError(t, deployments_service.ReevaluateWaiting(t.Context(), requested+11*60))
	assert.NotEmpty(t, hubAuditEvents(t, deployments_model.AuditChecksPassed), "both the lock and the timer have cleared")
}

// TestDeploymentsChecksAutoPromoteFollowsASuccess: once qa succeeds, an environment
// depending on it with auto_promote set gets the auto_promoted audit event, and Promote
// actually dispatches staging — proven by a real staging deployment row, not only the audit
// event. Replaying the identical qa success event creates neither a second staging row nor a
// second auto_promoted event.
func TestDeploymentsChecksAutoPromoteFollowsASuccess(t *testing.T) {
	// seedDeployableRelease commits through Gitea's own push path, whose pre-receive hook
	// calls back into the internal API, so the server has to be running.
	onGiteaRun(t, func(t *testing.T, _ *url.URL) {
		sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		repo, tag := seedDeployableRelease(t, sender, "staging")
		writeChecksEnvironment(t, &deployments_model.Environment{RepoID: repo.ID, Name: "qa", SortOrder: 10})
		writeChecksEnvironment(t, &deployments_model.Environment{
			RepoID: repo.ID, Name: "staging", SortOrder: 20, DependsOn: []string{"qa"}, AutoPromote: true,
		})
		before := len(hubAuditEvents(t, deployments_model.AuditAutoPromoted))

		recordRunEvent(t, repo, sender, "qa", tag, 9401, actions_model.StatusWaiting)
		recordRunEvent(t, repo, sender, "qa", tag, 9401, actions_model.StatusSuccess)

		events := hubAuditEvents(t, deployments_model.AuditAutoPromoted)
		require.Len(t, events, before+1)
		assert.Equal(t, "staging", events[len(events)-1].Environment)
		assert.Equal(t, tag, events[len(events)-1].ReleaseTag)
		assert.Contains(t, events[len(events)-1].Reason, "qa")
		require.Len(t, hubDeploymentsFor(t, repo.ID, "staging", tag), 1, "Promote actually dispatched staging, not only recorded that it meant to")

		// Replaying the same qa success event a second time — the notifier's own retry path,
		// or a duplicate webhook delivery — promotes nothing further: autoPromoteOne's own
		// DeploymentExists skip is what makes this idempotent.
		recordRunEvent(t, repo, sender, "qa", tag, 9401, actions_model.StatusSuccess)
		assert.Len(t, hubAuditEvents(t, deployments_model.AuditAutoPromoted), before+1, "no second auto_promoted event")
		assert.Len(t, hubDeploymentsFor(t, repo.ID, "staging", tag), 1, "no second staging row")
	})
}

// TestDeploymentsChecksAutoPromoteRefusedIntoReleasesOnlyWritesNothing: a prerelease auto-
// promoted into a releases_only environment is refused by Promote before anything dispatches,
// so there is nothing for auto_promote to be credited with — no audit row and no deployment.
func TestDeploymentsChecksAutoPromoteRefusedIntoReleasesOnlyWritesNothing(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	writeChecksEnvironment(t, &deployments_model.Environment{RepoID: repo.ID, Name: "qa", SortOrder: 10})
	writeChecksEnvironment(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "staging", SortOrder: 20, DependsOn: []string{"qa"}, AutoPromote: true, ReleasesOnly: true,
	})
	before := len(hubAuditEvents(t, deployments_model.AuditAutoPromoted))

	recordRunEvent(t, repo, sender, "qa", promotionPrerelease, 9601, actions_model.StatusWaiting)
	recordRunEvent(t, repo, sender, "qa", promotionPrerelease, 9601, actions_model.StatusSuccess)

	assert.Len(t, hubAuditEvents(t, deployments_model.AuditAutoPromoted), before,
		"staging refused the prerelease; auto_promote credits nothing that never happened")
	assert.Empty(t, hubDeploymentsFor(t, repo.ID, "staging", promotionPrerelease), "nothing dispatched into staging")
}

// TestDeploymentsChecksAutoPromoteWaitsForEveryDependency: with two dependencies, a success on
// only one of them promotes nothing.
func TestDeploymentsChecksAutoPromoteWaitsForEveryDependency(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	writeChecksEnvironment(t, &deployments_model.Environment{RepoID: repo.ID, Name: "qa", SortOrder: 10})
	writeChecksEnvironment(t, &deployments_model.Environment{RepoID: repo.ID, Name: "uat", SortOrder: 15})
	writeChecksEnvironment(t, &deployments_model.Environment{
		RepoID: repo.ID, Name: "staging", SortOrder: 20, DependsOn: []string{"qa", "uat"}, AutoPromote: true,
	})

	recordRunEvent(t, repo, sender, "qa", promotionFullRelease, 9501, actions_model.StatusWaiting)
	recordRunEvent(t, repo, sender, "qa", promotionFullRelease, 9501, actions_model.StatusSuccess)

	assert.Empty(t, hubAuditEvents(t, deployments_model.AuditAutoPromoted), "uat has not held the release yet")
	assert.Empty(t, hubDeploymentsFor(t, repo.ID, "staging", promotionFullRelease))
}

// TestDeploymentsChecksAutoPromoteIntoAWaitTimerAttributesAndLaterDispatches: an auto-promote
// whose checks answer wait still gets its audit — the waiting placeholder row already exists,
// so auto_promoted attaches to it exactly as it would a dispatched run — and once the timer
// elapses the sweeper's own dispatch reports checks_passed as it would for any waiting deploy.
func TestDeploymentsChecksAutoPromoteIntoAWaitTimerAttributesAndLaterDispatches(t *testing.T) {
	onGiteaRun(t, func(t *testing.T, _ *url.URL) {
		sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		repo, tag := seedDeployableRelease(t, sender, "staging")
		writeChecksEnvironment(t, &deployments_model.Environment{RepoID: repo.ID, Name: "qa", SortOrder: 10})
		writeChecksEnvironment(t, &deployments_model.Environment{
			RepoID: repo.ID, Name: "staging", SortOrder: 20, DependsOn: []string{"qa"}, AutoPromote: true, WaitMinutes: 10,
		})

		recordRunEvent(t, repo, sender, "qa", tag, 9701, actions_model.StatusWaiting)
		recordRunEvent(t, repo, sender, "qa", tag, 9701, actions_model.StatusSuccess)

		rows := hubDeploymentsFor(t, repo.ID, "staging", tag)
		require.Len(t, rows, 1, "the waiting placeholder was appended")
		assert.Equal(t, "waiting", rows[0].Status)

		events := hubAuditEvents(t, deployments_model.AuditAutoPromoted)
		require.NotEmpty(t, events, "the waiting row still gets credited to auto_promote")
		last := events[len(events)-1]
		assert.Equal(t, "staging", last.Environment)
		assert.Equal(t, tag, last.ReleaseTag)
		assert.Equal(t, rows[0].RunID, last.RunID, "the audit attaches to the placeholder's own row")

		requested := int64(rows[0].CreatedUnix)
		require.NoError(t, deployments_service.ReevaluateWaiting(t.Context(), requested+1))
		assert.Empty(t, hubAuditEvents(t, deployments_model.AuditChecksPassed), "the timer has not elapsed yet")

		require.NoError(t, deployments_service.ReevaluateWaiting(t.Context(), requested+11*60))
		assert.NotEmpty(t, hubAuditEvents(t, deployments_model.AuditChecksPassed), "the timer elapsed; the sweeper dispatched")
	})
}

// TestDeploymentsChecksCycleRefusesTheWriteAndLeavesTheRowUnchanged: a depends_on cycle is
// refused at 422 and the existing row is not updated.
func TestDeploymentsChecksCycleRefusesTheWriteAndLeavesTheRowUnchanged(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	staging := &deployments_model.Environment{RepoID: repo.ID, Name: "staging", SortOrder: 40}
	writeChecksEnvironment(t, staging)
	prod := &deployments_model.Environment{RepoID: repo.ID, Name: "prod", SortOrder: 50, DependsOn: []string{"staging"}}
	writeChecksEnvironment(t, prod)
	token := hubWriteToken(t, "user2")

	// staging -> prod closes the loop prod -> staging already opened: a two-hop cycle the
	// direct self-reference check in ValidatePromotionPolicy does not catch.
	body := map[string]any{
		"name": "staging", "sort_order": 40, "review_policy": "none", "required_reviewers": 1,
		"depends_on": []string{"prod"},
	}
	req := NewRequestWithJSON(t, "PUT", deploymentsv1.BasePath+"/environments/"+strconv.FormatInt(staging.ID, 10), body).AddTokenAuth(token)
	resp := MakeRequest(t, req, NoExpectedStatus)
	require.Equal(t, http.StatusUnprocessableEntity, resp.Code)
	var refused hubRefusal
	DecodeJSON(t, resp, &refused)
	assert.Equal(t, "cycle", refused.Code)
	assert.NotEmpty(t, refused.SuggestedAction)

	reloaded, err := deployments_model.GetEnvironmentByID(t.Context(), staging.ID)
	require.NoError(t, err)
	assert.Empty(t, reloaded.DependsOn, "the refused write left the row exactly as it was")
}

// TestDeploymentsChecksDependsOnDoesNotCrossRepositories: an environment name that exists only
// in another repository is outside this repository's effective set, so depends_on naming it is
// refused exactly as it would be for a name that exists nowhere at all.
func TestDeploymentsChecksDependsOnDoesNotCrossRepositories(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	otherRepo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 2})
	writeChecksEnvironment(t, &deployments_model.Environment{RepoID: otherRepo.ID, Name: "only-in-repo-2", SortOrder: 10})

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	body := map[string]any{
		"repo_id": repo.ID, "name": "cross-repo-prod", "sort_order": 60, "review_policy": "none", "required_reviewers": 1,
		"depends_on": []string{"only-in-repo-2"},
	}
	req := NewRequestWithJSON(t, "POST", deploymentsv1.BasePath+"/environments", body).AddTokenAuth(hubWriteToken(t, "user2"))
	resp := MakeRequest(t, req, NoExpectedStatus)
	require.Equal(t, http.StatusUnprocessableEntity, resp.Code)
	var refused hubRefusal
	DecodeJSON(t, resp, &refused)
	assert.Contains(t, refused.Message, "only-in-repo-2")
	assert.Contains(t, refused.Message, "does not exist", "repo 2's environment is not part of repo 1's effective set")
}

// TestDeploymentsChecksEnvironmentValidationCodes tables bad_wait, bad_window and bad_contexts
// on create: each is refused at 422 with its own code, and nothing is written.
func TestDeploymentsChecksEnvironmentValidationCodes(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := hubWriteToken(t, "user2")
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})

	cases := []struct {
		name    string
		envName string
		body    map[string]any
		code    string
	}{
		{"bad_wait", "bad-wait", map[string]any{"repo_id": repo.ID, "name": "bad-wait", "wait_minutes": 10081}, "bad_wait"},
		{
			"bad_window", "bad-window", map[string]any{
				"repo_id": repo.ID, "name": "bad-window",
				"deploy_window_days_mask": 128, "deploy_window_to_minute": 60, "deploy_window_timezone": "UTC",
			}, "bad_window",
		},
		{"bad_contexts", "bad-contexts", map[string]any{"repo_id": repo.ID, "name": "bad-contexts", "required_status_contexts": []string{""}}, "bad_contexts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := NewRequestWithJSON(t, "POST", deploymentsv1.BasePath+"/environments", c.body).AddTokenAuth(token)
			resp := MakeRequest(t, req, NoExpectedStatus)
			require.Equal(t, http.StatusUnprocessableEntity, resp.Code)
			var refused hubRefusal
			DecodeJSON(t, resp, &refused)
			assert.Equal(t, c.code, refused.Code)
			assert.NotEmpty(t, refused.SuggestedAction)

			_, err := deployments_model.GetEnvironment(t.Context(), repo.ID, c.envName)
			assert.Error(t, err, "nothing was written")
		})
	}
}

// TestDeploymentsChecksWaitMinutesBoundary: 10080 is accepted, 10081 is refused.
func TestDeploymentsChecksWaitMinutesBoundary(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := hubWriteToken(t, "user2")
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})

	req := NewRequestWithJSON(t, "POST", deploymentsv1.BasePath+"/environments",
		map[string]any{"repo_id": repo.ID, "name": "boundary-ok", "wait_minutes": 10080}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusCreated)

	req = NewRequestWithJSON(t, "POST", deploymentsv1.BasePath+"/environments",
		map[string]any{"repo_id": repo.ID, "name": "boundary-bad", "wait_minutes": 10081}).AddTokenAuth(token)
	resp := MakeRequest(t, req, NoExpectedStatus)
	require.Equal(t, http.StatusUnprocessableEntity, resp.Code)
}

// TestDeploymentsChecksRequiredStatusContextsBoundary: 20 entries are accepted, 21 are refused.
func TestDeploymentsChecksRequiredStatusContextsBoundary(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := hubWriteToken(t, "user2")
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})

	twenty := make([]string, 20)
	for i := range twenty {
		twenty[i] = "ci/check"
	}
	req := NewRequestWithJSON(t, "POST", deploymentsv1.BasePath+"/environments",
		map[string]any{"repo_id": repo.ID, "name": "contexts-ok", "required_status_contexts": twenty}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusCreated)

	twentyOne := append(append([]string(nil), twenty...), "ci/one-more")
	req = NewRequestWithJSON(t, "POST", deploymentsv1.BasePath+"/environments",
		map[string]any{"repo_id": repo.ID, "name": "contexts-bad", "required_status_contexts": twentyOne}).AddTokenAuth(token)
	resp := MakeRequest(t, req, NoExpectedStatus)
	require.Equal(t, http.StatusUnprocessableEntity, resp.Code)
}
