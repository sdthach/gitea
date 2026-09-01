// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"testing"

	actions_model "gitea.dev/models/actions"
	delivery_model "gitea.dev/models/delivery"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xorm.io/builder"
)

func TestDeliveryEnvironmentFromWorkflowID(t *testing.T) {
	cases := map[string]string{
		"deploy-prod.yaml":                  "prod",
		"deploy-qa.yml":                     "qa",
		"deploy-PROD.yaml":                  "prod",
		".gitea/workflows/deploy-uat.yaml":  "uat",
		"deploy-staging.yaml":               "staging",
		"build.yaml":                        "",
		"release.yml":                       "",
		"":                                  "",
		"deploy-prod":                       "",
		"deploy-.yaml":                      "",
		"predeploy-prod.yaml":               "",
		".gitea/workflows/deploy-prod.json": "",
	}
	for workflowID, want := range cases {
		assert.Equal(t, want, EnvironmentFromWorkflowID(workflowID), "workflow %q", workflowID)
	}
}

func TestDeliveryEventForRunStatus(t *testing.T) {
	cases := map[actions_model.Status]string{
		actions_model.StatusWaiting:   delivery_model.AuditRequested,
		actions_model.StatusBlocked:   delivery_model.AuditRequested,
		actions_model.StatusRunning:   delivery_model.AuditStarted,
		actions_model.StatusSuccess:   delivery_model.AuditSucceeded,
		actions_model.StatusFailure:   delivery_model.AuditFailed,
		actions_model.StatusCancelled: delivery_model.AuditCancelled,
		actions_model.StatusSkipped:   delivery_model.AuditCancelled,
		// An unmapped state records nothing rather than a guess: the log is evidence.
		actions_model.StatusUnknown: "",
	}
	for status, want := range cases {
		assert.Equal(t, want, EventForRunStatus(status), "status %s", status)
	}
}

func deployRun(workflowID, ref string, status actions_model.Status) *actions_model.ActionRun {
	return &actions_model.ActionRun{
		ID: 4242, RepoID: 1, WorkflowID: workflowID, Ref: ref, Status: status,
		CommitSHA: "0123456789abcdef0123456789abcdef01234567", Updated: timeutil.TimeStamp(1700),
	}
}

func TestDeliveryRecordsForRun(t *testing.T) {
	repo := &repo_model.Repository{ID: 1, OwnerName: "user2", Name: "repo1"}
	sender := &user_model.User{ID: 2, Name: "user2"}

	t.Run("a deploy run records a deployment and an audit event", func(t *testing.T) {
		deployment, audit, ok := RecordsForRun(repo, sender, deployRun("deploy-prod.yaml", "refs/tags/v1.2.3", actions_model.StatusSuccess))
		require.True(t, ok)

		assert.Equal(t, "prod", deployment.Environment)
		assert.Equal(t, "v1.2.3", deployment.ReleaseTag, "a deployment points at a release tag, never a parsed version string (D1)")
		assert.Equal(t, int64(4242), deployment.RunID)
		assert.Equal(t, "success", deployment.Status)
		assert.Empty(t, deployment.Branch)

		assert.Equal(t, delivery_model.AuditSucceeded, audit.Event)
		assert.Equal(t, delivery_model.SourceNotifier, audit.Source)
		assert.Equal(t, int64(2), audit.ActorID)
		assert.Equal(t, "user2", audit.ActorLogin, "the login is denormalized so deleting the user does not erase who deployed (SC 19)")
		assert.Equal(t, int64(1700), int64(audit.OccurredUnix))
		assert.Equal(t, deployment.RunURL, audit.RunURL)
	})

	t.Run("a push-triggered run is recorded by the same hook with source notifier", func(t *testing.T) {
		// E11: one code path, so the grid is complete by construction rather than by
		// reconciliation. Nothing in the input says who started it.
		run := deployRun("deploy-qa.yaml", "refs/tags/v2", actions_model.StatusRunning)
		run.TriggerUser = &user_model.User{ID: 4, Name: "user4"}
		_, audit, ok := RecordsForRun(repo, nil, run)
		require.True(t, ok)

		assert.Equal(t, delivery_model.SourceNotifier, audit.Source)
		assert.Equal(t, delivery_model.AuditStarted, audit.Event)
		assert.Equal(t, int64(4), audit.ActorID, "with no sender the run's own trigger user names the actor")
		assert.Equal(t, "user4", audit.ActorLogin)
	})

	t.Run("a run that is not a deploy records nothing", func(t *testing.T) {
		_, _, ok := RecordsForRun(repo, sender, deployRun("build.yaml", "refs/tags/v1", actions_model.StatusSuccess))
		assert.False(t, ok, "a repository with no deploy workflow gets no rows at all")
	})

	t.Run("a deploy dispatched against a branch records nothing", func(t *testing.T) {
		_, _, ok := RecordsForRun(repo, sender, deployRun("deploy-prod.yaml", "refs/heads/main", actions_model.StatusSuccess))
		assert.False(t, ok, "a deploy with no release identity has nothing to place in the grid (D1, D5)")
	})

	t.Run("an unmapped run status records nothing", func(t *testing.T) {
		_, _, ok := RecordsForRun(repo, sender, deployRun("deploy-prod.yaml", "refs/tags/v1", actions_model.StatusUnknown))
		assert.False(t, ok)
	})

	t.Run("no repository and no run record nothing", func(t *testing.T) {
		_, _, ok := RecordsForRun(nil, sender, deployRun("deploy-prod.yaml", "refs/tags/v1", actions_model.StatusSuccess))
		assert.False(t, ok)
		_, _, ok = RecordsForRun(repo, sender, nil)
		assert.False(t, ok)
	})
}

// TestDeliveryNotifierWritesTheLog exercises the exported entry point Gitea's own notifier
// registry calls — not the pure helper underneath it — so a wrapper that dropped the write
// could not pass.
func TestDeliveryNotifierWritesTheLog(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	n := NewNotifier()

	// Each deploy writes requested and a terminal event, so request-to-live latency is
	// derivable from the log alone.
	requested := deployRun("deploy-qa.yaml", "refs/tags/v1", actions_model.StatusWaiting)
	requested.Updated = timeutil.TimeStamp(2000)
	n.WorkflowRunStatusUpdate(ctx, repo, sender, requested)

	succeeded := deployRun("deploy-qa.yaml", "refs/tags/v1", actions_model.StatusSuccess)
	succeeded.Updated = timeutil.TimeStamp(2030)
	n.WorkflowRunStatusUpdate(ctx, repo, sender, succeeded)

	events, err := delivery_model.FindAuditEvents(ctx, builder.Eq{"repo_id": repo.ID}, "occurred_unix ASC, id ASC", 0)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, delivery_model.AuditRequested, events[0].Event)
	assert.Equal(t, delivery_model.AuditSucceeded, events[1].Event)
	assert.Equal(t, int64(30), int64(events[1].OccurredUnix-events[0].OccurredUnix))
	assert.Equal(t, delivery_model.SourceNotifier, events[0].Source)

	deployments, err := delivery_model.FindDeployments(ctx, builder.Eq{"repo_id": repo.ID}, "id ASC", 0)
	require.NoError(t, err)
	require.Len(t, deployments, 1, "one run is one deployment however many status changes it reports")
	assert.Equal(t, "qa", deployments[0].Environment)
	assert.Equal(t, "v1", deployments[0].ReleaseTag)

	// A run that is not a deploy leaves the log exactly as it was.
	n.WorkflowRunStatusUpdate(ctx, repo, sender, deployRun("build.yaml", "refs/tags/v1", actions_model.StatusSuccess))
	events, err = delivery_model.FindAuditEvents(ctx, builder.Eq{"repo_id": repo.ID}, "id ASC", 0)
	require.NoError(t, err)
	assert.Len(t, events, 2)
}
