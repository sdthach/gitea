// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"testing"

	"gitea.dev/models/db"
	deployments_model "gitea.dev/models/deployments"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xorm.io/builder"
)

// TestDeploymentsCanApproveEnvironment covers the approver set in BOTH its accepting
// and its refusing case for every branch. The default is whoever Gitea already permits to
// dispatch; the allowlist narrows it, and it is the SAME allowlist branch protection uses.
func TestDeploymentsCanApproveEnvironment(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	// user2 is a member of team 1; user4 is not.
	member := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	outsider := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})

	cases := []struct {
		name        string
		env         *deployments_model.Environment
		user        *user_model.User
		isRepoAdmin bool
		canDispatch bool
		want        bool
	}{
		{"nil environment refuses", nil, member, true, true, false},
		{"nil user refuses", &deployments_model.Environment{Name: "prod"}, nil, true, true, false},
		{
			"the default set is whoever may dispatch",
			&deployments_model.Environment{Name: "prod"}, outsider, false, true, true,
		},
		{
			"a user who may not dispatch is refused",
			&deployments_model.Environment{Name: "prod"}, outsider, false, false, false,
		},
		{
			"a repository admin passes when admins_can_bypass is set",
			&deployments_model.Environment{Name: "prod", AdminsCanBypass: true}, outsider, true, false, true,
		},
		{
			"admins_can_bypass false drops the admin back to the dispatch set",
			&deployments_model.Environment{Name: "prod"}, outsider, true, false, false,
		},
		{
			"admins_can_bypass false still lets a dispatcher approve",
			&deployments_model.Environment{Name: "prod"}, outsider, true, true, true,
		},
		{
			"an allowlisted user passes even without dispatch rights",
			&deployments_model.Environment{Name: "prod", RestrictReviewers: true, ReviewerUserIDs: []int64{outsider.ID}},
			outsider, false, false, true,
		},
		{
			"the allowlist refuses a dispatcher who is not on it",
			&deployments_model.Environment{Name: "prod", RestrictReviewers: true, ReviewerUserIDs: []int64{999}},
			outsider, false, true, false,
		},
		{
			"an allowlisted team's member passes",
			&deployments_model.Environment{Name: "prod", RestrictReviewers: true, ReviewerTeamIDs: []int64{1}},
			member, false, false, true,
		},
		{
			"a non-member of the allowlisted team is refused",
			&deployments_model.Environment{Name: "prod", RestrictReviewers: true, ReviewerTeamIDs: []int64{1}},
			outsider, false, true, false,
		},
		{
			"the allowlist with nobody on it refuses everyone but an admin",
			&deployments_model.Environment{Name: "prod", RestrictReviewers: true},
			member, false, true, false,
		},
		{
			"the allowlist still lets an admin who can bypass through",
			&deployments_model.Environment{Name: "prod", RestrictReviewers: true, AdminsCanBypass: true},
			member, true, false, true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want,
				CanApproveEnvironment(t.Context(), c.env, c.user, c.isRepoAdmin, c.canDispatch))
		})
	}
}

// TestDeploymentsApplyHeldRunsIsTheSecondHeldSource covers `⏸`'s second source: the
// reviews table repaints a cell that only looks queued, and repaints nothing else.
func TestDeploymentsApplyHeldRunsIsTheSecondHeldSource(t *testing.T) {
	cells := func() map[string][]Cell {
		return map[string][]Cell{
			"v1.0": {
				{Environment: "qa", State: CellInProgress, Symbol: "⟳", RunID: 100},
				{Environment: "prod", State: CellLive, Symbol: "✔ now", RunID: 101},
				{Environment: "uat", State: CellFailed, Symbol: "✗", RunID: 102},
				{Environment: "dev", State: CellNever, Symbol: "·"},
			},
		}
	}

	unchanged := applyHeldRuns(cells(), nil)
	assert.Equal(t, "⟳", unchanged["v1.0"][0].Symbol, "with nothing held the projection stands")

	held := applyHeldRuns(cells(), map[int64]bool{100: true, 101: true, 102: true})
	assert.Equal(t, CellHeld, held["v1.0"][0].State, "a queued cell whose run is held renders ⏸")
	assert.Equal(t, "⏸", held["v1.0"][0].Symbol)
	assert.Equal(t, CellLive, held["v1.0"][1].State, "a succeeded cell is terminal; a stale hold must not repaint it")
	assert.Equal(t, CellFailed, held["v1.0"][2].State, "a failed cell is terminal too")
	assert.Equal(t, CellNever, held["v1.0"][3].State)

	other := applyHeldRuns(cells(), map[int64]bool{999: true})
	assert.Equal(t, CellInProgress, other["v1.0"][0].State, "another run's hold is not this cell's")
}

// decideFixture stands up one held deploy in a gated environment.
func decideFixture(t *testing.T, policy string, required int64) (*deployments_model.Review, *deployments_model.Environment) {
	t.Helper()
	require.NoError(t, unittest.PrepareTestDatabase())

	env := &deployments_model.Environment{
		RepoID: 1, Name: "prod", ReviewPolicy: policy, RequiredReviewers: required,
	}
	// The record has to be in the database as well as in hand: the projection reads the
	// policy back from the environment record, never from what the caller passed.
	require.NoError(t, db.Insert(t.Context(), env))
	hold := &deployments_model.Review{
		RepoID: 1, Environment: "prod", RunID: 4242, JobID: 84, ReleaseTag: "v1.1",
		RequesterID: 2, RequesterLogin: "user2",
	}
	require.NoError(t, deployments_model.AppendReview(t.Context(), hold))
	return hold, env
}

// TestDeploymentsDecideWritesAnAuditEventAndReleases: a review releases the
// job and lands in the append-only log naming the approver and the time.
func TestDeploymentsDecideWritesAnAuditEventAndReleases(t *testing.T) {
	hold, env := decideFixture(t, deployments_model.PolicyAnyApprover, 1)
	approver := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})

	decision, err := Decide(t.Context(), ReviewRequest{
		Review: hold, Environment: env, Actor: approver,
		Event: deployments_model.AuditApproved, CanDispatch: true,
	})
	require.NoError(t, err)
	assert.Equal(t, deployments_model.ReviewApproved, decision.State)
	assert.Equal(t, int64(1), decision.ReviewsCount)
	assert.Equal(t, int64(1), decision.RequiredReviewers)

	votes, err := deployments_model.VotesForReview(t.Context(), hold)
	require.NoError(t, err)
	require.Len(t, votes, 1)
	assert.Equal(t, approver.ID, votes[0].ActorID)

	// The event itself names the approver and when, and it is on the same append-only log
	// every other deployment event is on.
	events, err := deployments_model.FindAuditEvents(t.Context(),
		builder.Eq{"run_id": int64(4242)}, "id ASC", 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "user4", events[0].ActorLogin)
	assert.NotZero(t, events[0].OccurredUnix)
	assert.Equal(t, "v1.1", events[0].ReleaseTag)
}

// TestDeploymentsDecideRefusals: every refusal is made by the service, not
// hidden in a view, and each carries a suggested next action.
func TestDeploymentsDecideRefusals(t *testing.T) {
	t.Run("a user who may not approve is refused", func(t *testing.T) {
		hold, env := decideFixture(t, deployments_model.PolicyAnyApprover, 1)
		outsider := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		_, err := Decide(t.Context(), ReviewRequest{
			Review: hold, Environment: env, Actor: outsider,
			Event: deployments_model.AuditApproved, CanDispatch: false,
		})
		assertRefused(t, err)
	})

	t.Run("others_only refuses the requester's own review", func(t *testing.T) {
		hold, env := decideFixture(t, deployments_model.PolicyOthersOnly, 1)
		requester := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		_, err := Decide(t.Context(), ReviewRequest{
			Review: hold, Environment: env, Actor: requester,
			Event: deployments_model.AuditApproved, CanDispatch: true,
		})
		assertRefused(t, err)

		// A second user's review is accepted, which is the other half.
		other := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		decision, err := Decide(t.Context(), ReviewRequest{
			Review: hold, Environment: env, Actor: other,
			Event: deployments_model.AuditApproved, CanDispatch: true,
		})
		require.NoError(t, err)
		assert.Equal(t, deployments_model.ReviewApproved, decision.State)
	})

	t.Run("an environment with no policy holds nothing to approve", func(t *testing.T) {
		hold, env := decideFixture(t, deployments_model.PolicyNone, 1)
		approver := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		_, err := Decide(t.Context(), ReviewRequest{
			Review: hold, Environment: env, Actor: approver,
			Event: deployments_model.AuditApproved, CanDispatch: true,
		})
		assertRefused(t, err)
	})

	t.Run("approving twice as the same user is refused", func(t *testing.T) {
		hold, env := decideFixture(t, deployments_model.PolicyAnyApprover, 2)
		approver := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		req := ReviewRequest{
			Review: hold, Environment: env, Actor: approver,
			Event: deployments_model.AuditApproved, CanDispatch: true,
		}
		decision, err := Decide(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, deployments_model.ReviewPending, decision.State,
			"required_reviewers 2 keeps the job held after the first review")

		_, err = Decide(t.Context(), req)
		assertRefused(t, err)
	})

	t.Run("a rejection ends the deploy and no later review revives it", func(t *testing.T) {
		hold, env := decideFixture(t, deployments_model.PolicyAnyApprover, 1)
		approver := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		decision, err := Decide(t.Context(), ReviewRequest{
			Review: hold, Environment: env, Actor: approver,
			Event: deployments_model.AuditRejected, Reason: "not this release", CanDispatch: true,
		})
		require.NoError(t, err)
		assert.Equal(t, deployments_model.ReviewRejected, decision.State)

		second := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		_, err = Decide(t.Context(), ReviewRequest{
			Review: hold, Environment: env, Actor: second,
			Event: deployments_model.AuditApproved, CanDispatch: true,
		})
		assertRefused(t, err)
	})

	t.Run("a verb that is neither approve nor reject is refused", func(t *testing.T) {
		hold, env := decideFixture(t, deployments_model.PolicyAnyApprover, 1)
		approver := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		_, err := Decide(t.Context(), ReviewRequest{
			Review: hold, Environment: env, Actor: approver,
			Event: deployments_model.AuditStarted, CanDispatch: true,
		})
		assertRefused(t, err)
	})
}

// TestDeploymentsPendingReviewRunsFeedsTheGrid proves the grid's second `⏸` source reads the
// same projection the gate does: a run stays listed while it is pending and drops out once
// it is approved.
func TestDeploymentsPendingReviewRunsFeedsTheGrid(t *testing.T) {
	hold, env := decideFixture(t, deployments_model.PolicyAnyApprover, 1)

	pending, err := PendingReviewRuns(t.Context(), 1)
	require.NoError(t, err)
	assert.True(t, pending[hold.RunID], "an unapproved hold is what the grid renders as ⏸")

	approver := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
	_, err = Decide(t.Context(), ReviewRequest{
		Review: hold, Environment: env, Actor: approver,
		Event: deployments_model.AuditApproved, CanDispatch: true,
	})
	require.NoError(t, err)

	pending, err = PendingReviewRuns(t.Context(), 1)
	require.NoError(t, err)
	assert.False(t, pending[hold.RunID], "an approved deploy is no longer held")
}

func assertRefused(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	refused, ok := err.(*ErrReviewRefused)
	require.True(t, ok, "a refusal the caller is not permitted to make must be an ErrReviewRefused, so the API answers 403 rather than 500: got %v", err)
	assert.NotEmpty(t, refused.Err.Message)
	assert.NotEmpty(t, refused.Err.SuggestedAction, "every error carries a suggested next action")
}
