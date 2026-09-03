// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"testing"

	actions_model "gitea.dev/models/actions"
	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	deployments_model "gitea.dev/models/deployments"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	deploymentsv1 "gitea.dev/routers/api/deployments/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reviewRun inserts one waiting deploy run at a release tag, reading its workflow from the
// commit the run records — the same path production takes, because jobparser.Job carries no
// environment field and the declaration has to be re-read from the file.
func reviewRun(t *testing.T, repo *repo_model.Repository, sha string, index int64) *actions_model.ActionRun {
	t.Helper()
	run := &actions_model.ActionRun{
		Title: "deploy v1.1", RepoID: repo.ID, OwnerID: repo.OwnerID,
		WorkflowID: "deploy.yaml", Index: index, TriggerUserID: 2,
		Ref: "refs/tags/v1.1", CommitSHA: sha,
		Event: "workflow_dispatch", TriggerEvent: "workflow_dispatch",
		Status:         actions_model.StatusWaiting,
		WorkflowRepoID: repo.ID, WorkflowCommitSHA: sha,
	}
	require.NoError(t, db.Insert(t.Context(), run))
	return run
}

// reviewJob inserts one waiting job of a run. jobID names the job in the workflow file, so
// "to-prod" declares environment prod and "build" declares none.
func reviewJob(t *testing.T, run *actions_model.ActionRun, jobID, name string) *actions_model.ActionRunJob {
	t.Helper()
	job := &actions_model.ActionRunJob{
		RunID: run.ID, RepoID: run.RepoID, OwnerID: run.OwnerID, CommitSHA: run.CommitSHA,
		Name: name, Attempt: 1, JobID: jobID,
		Status: actions_model.StatusWaiting, RunsOn: []string{"ubuntu-latest"},
		WorkflowPayload: []byte("on: workflow_dispatch\njobs:\n  " + jobID + ":\n    runs-on: ubuntu-latest\n"),
	}
	require.NoError(t, db.Insert(t.Context(), job))
	return job
}

func reviewRunner(t *testing.T, repo *repo_model.Repository, name string) *actions_model.ActionRunner {
	t.Helper()
	runner := &actions_model.ActionRunner{
		UUID: "review-runner-" + name, Name: "review-runner-" + name,
		RepoID: repo.ID, AgentLabels: []string{"ubuntu-latest"},
	}
	runner.GenerateAndFillToken()
	require.NoError(t, db.Insert(t.Context(), runner))
	return runner
}

// gridCell reads one release × environment cell over the documented grid endpoint, which is
// the same projection the page renders.
func gridCell(t *testing.T, token string, repoID int64, releaseTag, environment string) (state, symbol string) {
	t.Helper()
	var rows []struct {
		ReleaseTag string `json:"release_tag"`
		Cells      []struct {
			Environment string `json:"environment"`
			State       string `json:"state"`
			Symbol      string `json:"symbol"`
		} `json:"cells"`
	}
	req := NewRequest(t, "GET", deploymentsv1.BasePath+"/deployments/matrix?repo_id="+strconv.FormatInt(repoID, 10)).AddTokenAuth(token)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &rows)
	for _, row := range rows {
		if row.ReleaseTag != releaseTag {
			continue
		}
		for _, cell := range row.Cells {
			if cell.Environment == environment {
				return cell.State, cell.Symbol
			}
		}
	}
	t.Fatalf("%s × %s is not a cell of the grid", releaseTag, environment)
	return "", ""
}

// TestDeliveryReviewGateHoldsJobsUntilApproved runs the gate through the
// production path: a real workflow file, real run and job rows, and Gitea's own
// CreateTaskForRunner — the function the spoke delegates from. Nothing here calls the gate
// directly, so a spoke that stopped delegating would fail every assertion.
func TestDeliveryReviewGateHoldsJobsUntilApproved(t *testing.T) {
	onGiteaRun(t, func(t *testing.T, _ *url.URL) {
		repo, sha := commitWorkflow(t)
		ctx := t.Context()

		// prod is gated; nothing else is, so an ordinary job is unaffected.
		require.NoError(t, db.Insert(ctx, &deployments_model.Environment{
			RepoID: repo.ID, Name: "prod", SortOrder: 50,
			ReviewPolicy: deployments_model.PolicyAnyApprover, RequiredReviewers: 1,
		}))
		require.NoError(t, db.Insert(ctx, &deployments_model.Environment{
			RepoID: repo.ID, Name: "qa", SortOrder: 20,
			ReviewPolicy: deployments_model.PolicyNone, RequiredReviewers: 1,
		}))

		session := loginUser(t, "user2")
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)
		runner := reviewRunner(t, repo, "main")

		run := reviewRun(t, repo, sha, 9901)
		gated := reviewJob(t, run, "to-prod", "to prod")
		ungated := reviewJob(t, run, "build", "build")

		// The gated job is oldest-first in the keyset walk, so a gate that narrowed by
		// reordering or restarting the cursor would show up as the ungated job never being
		// reached.
		require.Less(t, gated.ID, ungated.ID)

		task, ok, err := actions_model.CreateTaskForRunner(ctx, runner)
		require.NoError(t, err)
		require.True(t, ok, "the ungated job in the same page is still assignable")
		assert.Equal(t, ungated.ID, task.JobID, "the held job must not be the one handed out")

		_, ok, err = actions_model.CreateTaskForRunner(ctx, runner)
		require.NoError(t, err)
		assert.False(t, ok, "no runner picks up the held job")

		held := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: gated.ID})
		assert.Equal(t, actions_model.StatusWaiting, held.Status)
		assert.Zero(t, held.TaskID, "a held job is never assigned a task")

		t.Run("no runner claims the held job under a concurrent burst", func(t *testing.T) {
			// The gate has to hold under contention, not only in a single-threaded poll: a
			// check evaluated outside the claim would let one of these through.
			const runners = 4
			burst := make([]*actions_model.ActionRunner, runners)
			for i := range runners {
				burst[i] = reviewRunner(t, repo, "burst-"+strconv.Itoa(i))
			}
			// One free job per runner, so every runner has something it may legitimately
			// take; the only job none of them may take is the held one.
			free := map[int64]bool{}
			for i := range runners {
				free[reviewJob(t, run, "build", fmt.Sprintf("burst build %d", i)).ID] = true
			}

			type outcome struct {
				jobID int64
				ok    bool
				err   error
			}
			results := make([]outcome, runners)
			var wg sync.WaitGroup
			for i := range runners {
				wg.Go(func() {
					task, ok, err := actions_model.CreateTaskForRunner(t.Context(), burst[i])
					if task != nil {
						results[i] = outcome{task.JobID, ok, err}
						return
					}
					results[i] = outcome{0, ok, err}
				})
			}
			wg.Wait()

			claimed := 0
			for i, r := range results {
				require.NoError(t, r.err, "runner %d", i)
				if !r.ok {
					continue
				}
				claimed++
				assert.NotEqual(t, gated.ID, r.jobID, "runner %d claimed the held job", i)
				assert.True(t, free[r.jobID], "runner %d claimed a job that was not free", i)
			}
			assert.Positive(t, claimed, "the burst has to actually claim something, or it proves nothing")

			stillHeld := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: gated.ID})
			assert.Equal(t, actions_model.StatusWaiting, stillHeld.Status)
			assert.Zero(t, stillHeld.TaskID)
		})

		// The gate recorded the hold, and it is what the API publishes.
		var listed []struct {
			ID                int64  `json:"id"`
			Environment       string `json:"environment"`
			RunID             int64  `json:"run_id"`
			JobID             int64  `json:"job_id"`
			ReleaseTag        string `json:"release_tag"`
			RequesterLogin    string `json:"requester_login"`
			State             string `json:"state"`
			ReviewPolicy      string `json:"review_policy"`
			ReviewsCount      int64  `json:"reviews_count"`
			RequiredReviewers int64  `json:"required_reviewers"`
			CanApprove        bool   `json:"can_approve"`
			AgeSeconds        int64  `json:"age_seconds"`
		}
		req := NewRequest(t, "GET", deploymentsv1.BasePath+"/reviews?environment=prod").AddTokenAuth(token)
		DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &listed)
		require.Len(t, listed, 1, "one held job, one hold row")
		hold := listed[0]
		assert.Equal(t, "prod", hold.Environment)
		assert.Equal(t, gated.ID, hold.JobID)
		assert.Equal(t, run.ID, hold.RunID)
		assert.Equal(t, "v1.1", hold.ReleaseTag)
		assert.Equal(t, "user2", hold.RequesterLogin)
		assert.Equal(t, "pending", hold.State)
		assert.Equal(t, int64(0), hold.ReviewsCount)
		assert.Equal(t, int64(1), hold.RequiredReviewers)
		assert.True(t, hold.CanApprove, "user2 owns the repository, so the forge permits them to approve")
		assert.GreaterOrEqual(t, hold.AgeSeconds, int64(0))
		assert.Equal(t, deployments_model.PolicyAnyApprover, hold.ReviewPolicy,
			"the row says why it is held, so a client need not fetch the environment separately")

		t.Run("a hold expands its deployment", func(t *testing.T) {
			require.NoError(t, deployments_model.AppendDeployment(ctx, &deployments_model.Deployment{
				RepoID: repo.ID, Environment: "prod", ReleaseTag: "v1.1",
				RunID: run.ID, Status: actions_model.StatusWaiting.String(),
			}))
			var expanded []struct {
				Deployment *deployments_model.Deployment `json:"deployment"`
			}
			req := NewRequest(t, "GET", deploymentsv1.BasePath+"/reviews?expand=deployment").AddTokenAuth(token)
			DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &expanded)
			require.Len(t, expanded, 1)
			require.NotNil(t, expanded[0].Deployment, "?expand=deployment resolves the run's deployment")
			assert.Equal(t, "v1.1", expanded[0].Deployment.ReleaseTag)
		})

		t.Run("the grid renders the held cell as ⏸ from the reviews table", func(t *testing.T) {
			// This is the case only the SECOND source can answer. The run has already
			// started — its ungated build job was claimed above, so the notifier records
			// `started` — which makes the projection over the log alone say "in progress".
			// The deploy job is nonetheless held, and the reviews table is what knows it.
			// A grid reading the log alone renders ⟳ here and is wrong.
			for _, event := range []string{deployments_model.AuditRequested, deployments_model.AuditStarted} {
				require.NoError(t, deployments_model.AppendAuditEvent(ctx, &deployments_model.AuditEvent{
					Event: event, RepoID: repo.ID, Environment: "prod",
					ReleaseTag: "v1.1", RunID: run.ID, Source: deployments_model.SourceNotifier,
				}))
			}
			state, symbol := gridCell(t, token, repo.ID, "v1.1", "prod")
			assert.Equal(t, "held", state)
			assert.Equal(t, "⏸", symbol)
		})

		t.Run("a user without review rights is refused at the endpoint", func(t *testing.T) {
			// Refused HERE, not merely offered no button. user4 can read the public
			// repository but has no write on its Actions unit.
			otherSession := loginUser(t, "user4")
			otherToken := getTokenForLoggedInUser(t, otherSession, auth_model.AccessTokenScopeWriteRepository)
			req := NewRequest(t, "POST",
				fmt.Sprintf("%s/reviews/%d/approve", deploymentsv1.BasePath, hold.ID)).AddTokenAuth(otherToken)
			resp := MakeRequest(t, req, http.StatusForbidden)
			var refusal struct {
				Message         string `json:"message"`
				SuggestedAction string `json:"suggested_action"`
			}
			DecodeJSON(t, resp, &refusal)
			assert.NotEmpty(t, refusal.Message)
			assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")

			// And the job is still held after the refused call.
			_, ok, err := actions_model.CreateTaskForRunner(ctx, runner)
			require.NoError(t, err)
			assert.False(t, ok)
		})

		t.Run("approving releases the job and lands in the audit log", func(t *testing.T) {
			req := NewRequest(t, "POST",
				fmt.Sprintf("%s/reviews/%d/approve", deploymentsv1.BasePath, hold.ID)).AddTokenAuth(token)
			var decided struct {
				State        string `json:"state"`
				ReviewsCount int64  `json:"reviews_count"`
			}
			DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &decided)
			assert.Equal(t, "approved", decided.State)
			assert.Equal(t, int64(1), decided.ReviewsCount)

			var events []*deployments_model.AuditEvent
			req = NewRequest(t, "GET", deploymentsv1.BasePath+"/audit?event=approved&sort_by=id&order=asc").AddTokenAuth(token)
			DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &events)
			require.Len(t, events, 1, "the review is an audit event on the append-only log")
			assert.Equal(t, "user2", events[0].ActorLogin)
			assert.Equal(t, int64(2), events[0].ActorID)
			assert.NotZero(t, events[0].OccurredUnix)

			task, ok, err := actions_model.CreateTaskForRunner(ctx, runner)
			require.NoError(t, err)
			require.True(t, ok, "an approved deploy proceeds")
			assert.Equal(t, gated.ID, task.JobID)

			// And the cell stops rendering ⏸ once nothing is held, which the projection
			// over the log alone cannot say either: its last event is still `started`.
			state, symbol := gridCell(t, token, repo.ID, "v1.1", "prod")
			assert.Equal(t, "in_progress", state)
			assert.Equal(t, "⟳", symbol)
		})
	})
}

// TestDeliveryReviewRejectionEndsTheDeploy is the other half: a rejection is terminal,
// the run does not proceed later, and the rejection names its approver on the audit log.
func TestDeliveryReviewRejectionEndsTheDeploy(t *testing.T) {
	onGiteaRun(t, func(t *testing.T, _ *url.URL) {
		repo, sha := commitWorkflow(t)
		ctx := t.Context()

		require.NoError(t, db.Insert(ctx, &deployments_model.Environment{
			RepoID: repo.ID, Name: "prod", SortOrder: 50,
			ReviewPolicy: deployments_model.PolicyOthersOnly, RequiredReviewers: 1,
		}))

		runner := reviewRunner(t, repo, "reject")
		run := reviewRun(t, repo, sha, 9902)
		gated := reviewJob(t, run, "to-prod", "to prod")

		_, ok, err := actions_model.CreateTaskForRunner(ctx, runner)
		require.NoError(t, err)
		require.False(t, ok, "the gate holds the job before anyone has decided")

		session := loginUser(t, "user2")
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

		var listed []struct {
			ID    int64  `json:"id"`
			State string `json:"state"`
		}
		req := NewRequest(t, "GET", deploymentsv1.BasePath+"/reviews").AddTokenAuth(token)
		DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &listed)
		require.Len(t, listed, 1)

		// user2 triggered the run and the policy is others_only, so their own review is
		// refused by the endpoint rather than silently ignored.
		req = NewRequest(t, "POST",
			fmt.Sprintf("%s/reviews/%d/approve", deploymentsv1.BasePath, listed[0].ID)).AddTokenAuth(token)
		MakeRequest(t, req, http.StatusForbidden)

		req = NewRequest(t, "POST",
			fmt.Sprintf("%s/reviews/%d/reject?reason=wrong+release", deploymentsv1.BasePath, listed[0].ID)).AddTokenAuth(token)
		var decided struct {
			State string `json:"state"`
		}
		DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &decided)
		assert.Equal(t, "rejected", decided.State)

		var events []*deployments_model.AuditEvent
		req = NewRequest(t, "GET", deploymentsv1.BasePath+"/audit?event=rejected").AddTokenAuth(token)
		DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &events)
		require.Len(t, events, 1)
		assert.Equal(t, "user2", events[0].ActorLogin)
		assert.NotZero(t, events[0].OccurredUnix)

		// The run does not proceed later: the rejected job is never assigned, however many
		// times a runner polls.
		for range 3 {
			_, ok, err := actions_model.CreateTaskForRunner(ctx, runner)
			require.NoError(t, err)
			assert.False(t, ok, "a rejected deploy is never handed to a runner")
		}
		stillHeld := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: gated.ID})
		assert.Zero(t, stillHeld.TaskID)
	})
}

// TestDeliveryReviewsPageIsAClientOfTheAPI covers the pending-review view: the
// handler serves the shell and every figure — requester, release, age, run link and whether
// the viewer may act — arrives over the documented endpoint.
func TestDeliveryReviewsPageIsAClientOfTheAPI(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/delivery/approvals")
	MakeRequest(t, req, http.StatusSeeOther)

	session := loginUser(t, "user2")
	req = NewRequest(t, "GET", "/delivery/approvals")
	resp := session.MakeRequest(t, req, http.StatusOK)
	body := resp.Body.String()
	assert.Contains(t, body, deploymentsv1.BasePath+"/reviews",
		"the page fetches its rows from the documented endpoint")
	assert.Contains(t, body, "suggested_action",
		"the page surfaces the API's suggested next action")
	assert.Contains(t, body, "can_approve",
		"a user without review rights is offered no action")

	req = NewRequest(t, "GET", "/delivery/environments/prod/approvals")
	resp = session.MakeRequest(t, req, http.StatusOK)
	assert.Contains(t, resp.Body.String(), `"prod"`,
		"the environment's own pending list is filtered to it, so an approver need not hunt the grid")
}

// TestDeliveryReviewGateLeavesUngatedRepositoriesAlone is the "with the fork absent"
// case as closely as an integration test can reach it: with every environment's policy at
// its default of none, the same run deploys with no gate at all.
func TestDeliveryReviewGateLeavesUngatedRepositoriesAlone(t *testing.T) {
	onGiteaRun(t, func(t *testing.T, _ *url.URL) {
		repo, sha := commitWorkflow(t)
		ctx := t.Context()

		gated, err := deployments_model.RepoHasGatedEnvironment(ctx, repo.ID)
		require.NoError(t, err)
		require.False(t, gated, "the seeded default environments all carry review_policy none")

		runner := reviewRunner(t, repo, "ungated")
		run := reviewRun(t, repo, sha, 9903)
		job := reviewJob(t, run, "to-prod", "to prod")

		task, ok, err := actions_model.CreateTaskForRunner(ctx, runner)
		require.NoError(t, err)
		require.True(t, ok, "a job declaring environment prod is assigned when nothing gates prod")
		assert.Equal(t, job.ID, task.JobID)

		count, err := db.GetEngine(ctx).Where("repo_id = ?", repo.ID).Count(new(deployments_model.Review))
		require.NoError(t, err)
		assert.Zero(t, count, "an ungated repository records no hold at all")
	})
}
