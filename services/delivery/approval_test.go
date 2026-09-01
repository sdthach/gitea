// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"testing"

	"gitea.dev/models/db"
	delivery_model "gitea.dev/models/delivery"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xorm.io/builder"
)

// TestDeliveryCanApproveEnvironment covers the approver set (F5a, F12) in BOTH its accepting
// and its refusing case for every branch. The default is whoever Gitea already permits to
// dispatch; the allowlist narrows it, and it is the SAME allowlist branch protection uses.
func TestDeliveryCanApproveEnvironment(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	// user2 is a member of team 1; user4 is not.
	member := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	outsider := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})

	cases := []struct {
		name        string
		env         *delivery_model.Environment
		user        *user_model.User
		isRepoAdmin bool
		canDispatch bool
		want        bool
	}{
		{"nil environment refuses", nil, member, true, true, false},
		{"nil user refuses", &delivery_model.Environment{Name: "prod"}, nil, true, true, false},
		{
			"the default set is whoever may dispatch",
			&delivery_model.Environment{Name: "prod"}, outsider, false, true, true,
		},
		{
			"a user who may not dispatch is refused",
			&delivery_model.Environment{Name: "prod"}, outsider, false, false, false,
		},
		{
			"a repository admin passes",
			&delivery_model.Environment{Name: "prod"}, outsider, true, false, true,
		},
		{
			"block_admin_override drops the admin back to the dispatch set",
			&delivery_model.Environment{Name: "prod", BlockAdminOverride: true}, outsider, true, false, false,
		},
		{
			"block_admin_override still lets a dispatcher approve",
			&delivery_model.Environment{Name: "prod", BlockAdminOverride: true}, outsider, true, true, true,
		},
		{
			"an allowlisted user passes even without dispatch rights",
			&delivery_model.Environment{Name: "prod", EnableBypassAllowlist: true, BypassAllowlistUserIDs: []int64{outsider.ID}},
			outsider, false, false, true,
		},
		{
			"the allowlist refuses a dispatcher who is not on it",
			&delivery_model.Environment{Name: "prod", EnableBypassAllowlist: true, BypassAllowlistUserIDs: []int64{999}},
			outsider, false, true, false,
		},
		{
			"an allowlisted team's member passes",
			&delivery_model.Environment{Name: "prod", EnableBypassAllowlist: true, BypassAllowlistTeamIDs: []int64{1}},
			member, false, false, true,
		},
		{
			"a non-member of the allowlisted team is refused",
			&delivery_model.Environment{Name: "prod", EnableBypassAllowlist: true, BypassAllowlistTeamIDs: []int64{1}},
			outsider, false, true, false,
		},
		{
			"the allowlist with nobody on it refuses everyone but an admin",
			&delivery_model.Environment{Name: "prod", EnableBypassAllowlist: true},
			member, false, true, false,
		},
		{
			"the allowlist still lets an unblocked admin through",
			&delivery_model.Environment{Name: "prod", EnableBypassAllowlist: true},
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

// TestDeliveryApplyHeldRunsIsTheSecondHeldSource covers `⏸`'s second source (E15): the
// approvals table repaints a cell that only looks queued, and repaints nothing else.
func TestDeliveryApplyHeldRunsIsTheSecondHeldSource(t *testing.T) {
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
func decideFixture(t *testing.T, policy string, required int64) (*delivery_model.Approval, *delivery_model.Environment) {
	t.Helper()
	require.NoError(t, unittest.PrepareTestDatabase())

	env := &delivery_model.Environment{
		RepoID: 1, Name: "prod", ApprovalPolicy: policy, RequiredApprovals: required,
	}
	// The record has to be in the database as well as in hand: the projection reads the
	// policy back from the environment record, never from what the caller passed (E15).
	require.NoError(t, db.Insert(t.Context(), env))
	hold := &delivery_model.Approval{
		RepoID: 1, Environment: "prod", RunID: 4242, JobID: 84, ReleaseTag: "v1.1",
		RequesterID: 2, RequesterLogin: "user2",
	}
	require.NoError(t, delivery_model.AppendApproval(t.Context(), hold))
	return hold, env
}

// TestDeliveryDecideWritesAnAuditEventAndReleases is F5c and SC 18: an approval releases the
// job and lands in the append-only log naming the approver and the time.
func TestDeliveryDecideWritesAnAuditEventAndReleases(t *testing.T) {
	hold, env := decideFixture(t, delivery_model.PolicyAnyApprover, 1)
	approver := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})

	decision, err := Decide(t.Context(), ApprovalRequest{
		Approval: hold, Environment: env, Actor: approver,
		Event: delivery_model.AuditApproved, CanDispatch: true,
	})
	require.NoError(t, err)
	assert.Equal(t, delivery_model.ApprovalApproved, decision.State)
	assert.Equal(t, int64(1), decision.ApprovalsCount)
	assert.Equal(t, int64(1), decision.RequiredApprovals)

	votes, err := delivery_model.VotesForApproval(t.Context(), hold)
	require.NoError(t, err)
	require.Len(t, votes, 1)
	assert.Equal(t, approver.ID, votes[0].ActorID)

	// The event itself names the approver and when, and it is on the same append-only log
	// every other delivery event is on.
	events, err := delivery_model.FindAuditEvents(t.Context(),
		builder.Eq{"run_id": int64(4242)}, "id ASC", 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "user4", events[0].ActorLogin)
	assert.NotZero(t, events[0].OccurredUnix)
	assert.Equal(t, "v1.1", events[0].ReleaseTag)
}

// TestDeliveryDecideRefusals is SC 21 and SC 20: every refusal is made by the service, not
// hidden in a view, and each carries a suggested next action.
func TestDeliveryDecideRefusals(t *testing.T) {
	t.Run("a user who may not approve is refused", func(t *testing.T) {
		hold, env := decideFixture(t, delivery_model.PolicyAnyApprover, 1)
		outsider := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		_, err := Decide(t.Context(), ApprovalRequest{
			Approval: hold, Environment: env, Actor: outsider,
			Event: delivery_model.AuditApproved, CanDispatch: false,
		})
		assertRefused(t, err)
	})

	t.Run("others_only refuses the requester's own approval", func(t *testing.T) {
		hold, env := decideFixture(t, delivery_model.PolicyOthersOnly, 1)
		requester := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		_, err := Decide(t.Context(), ApprovalRequest{
			Approval: hold, Environment: env, Actor: requester,
			Event: delivery_model.AuditApproved, CanDispatch: true,
		})
		assertRefused(t, err)

		// A second user's approval is accepted, which is the other half of SC 18.
		other := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		decision, err := Decide(t.Context(), ApprovalRequest{
			Approval: hold, Environment: env, Actor: other,
			Event: delivery_model.AuditApproved, CanDispatch: true,
		})
		require.NoError(t, err)
		assert.Equal(t, delivery_model.ApprovalApproved, decision.State)
	})

	t.Run("an environment with no policy holds nothing to approve", func(t *testing.T) {
		hold, env := decideFixture(t, delivery_model.PolicyNone, 1)
		approver := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		_, err := Decide(t.Context(), ApprovalRequest{
			Approval: hold, Environment: env, Actor: approver,
			Event: delivery_model.AuditApproved, CanDispatch: true,
		})
		assertRefused(t, err)
	})

	t.Run("approving twice as the same user is refused", func(t *testing.T) {
		hold, env := decideFixture(t, delivery_model.PolicyAnyApprover, 2)
		approver := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		req := ApprovalRequest{
			Approval: hold, Environment: env, Actor: approver,
			Event: delivery_model.AuditApproved, CanDispatch: true,
		}
		decision, err := Decide(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, delivery_model.ApprovalPending, decision.State,
			"required_approvals 2 keeps the job held after the first approval (SC 18)")

		_, err = Decide(t.Context(), req)
		assertRefused(t, err)
	})

	t.Run("a rejection ends the deploy and no later approval revives it", func(t *testing.T) {
		hold, env := decideFixture(t, delivery_model.PolicyAnyApprover, 1)
		approver := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		decision, err := Decide(t.Context(), ApprovalRequest{
			Approval: hold, Environment: env, Actor: approver,
			Event: delivery_model.AuditRejected, Reason: "not this release", CanDispatch: true,
		})
		require.NoError(t, err)
		assert.Equal(t, delivery_model.ApprovalRejected, decision.State)

		second := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		_, err = Decide(t.Context(), ApprovalRequest{
			Approval: hold, Environment: env, Actor: second,
			Event: delivery_model.AuditApproved, CanDispatch: true,
		})
		assertRefused(t, err)
	})

	t.Run("a verb that is neither approve nor reject is refused", func(t *testing.T) {
		hold, env := decideFixture(t, delivery_model.PolicyAnyApprover, 1)
		approver := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		_, err := Decide(t.Context(), ApprovalRequest{
			Approval: hold, Environment: env, Actor: approver,
			Event: delivery_model.AuditStarted, CanDispatch: true,
		})
		assertRefused(t, err)
	})
}

// TestDeliveryPendingApprovalRunsFeedsTheGrid proves the grid's second `⏸` source reads the
// same projection the gate does: a run stays listed while it is pending and drops out once
// it is approved.
func TestDeliveryPendingApprovalRunsFeedsTheGrid(t *testing.T) {
	hold, env := decideFixture(t, delivery_model.PolicyAnyApprover, 1)

	pending, err := PendingApprovalRuns(t.Context(), 1)
	require.NoError(t, err)
	assert.True(t, pending[hold.RunID], "an unapproved hold is what the grid renders as ⏸")

	approver := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
	_, err = Decide(t.Context(), ApprovalRequest{
		Approval: hold, Environment: env, Actor: approver,
		Event: delivery_model.AuditApproved, CanDispatch: true,
	})
	require.NoError(t, err)

	pending, err = PendingApprovalRuns(t.Context(), 1)
	require.NoError(t, err)
	assert.False(t, pending[hold.RunID], "an approved deploy is no longer held")
}

func assertRefused(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	refused, ok := err.(*ErrApprovalRefused)
	require.True(t, ok, "a refusal the caller is not permitted to make must be an ErrApprovalRefused, so the API answers 403 rather than 500: got %v", err)
	assert.NotEmpty(t, refused.Err.Message)
	assert.NotEmpty(t, refused.Err.SuggestedAction, "every error carries a suggested next action (A21)")
}
