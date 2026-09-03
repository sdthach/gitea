// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"context"
	"errors"
	"testing"

	actions_model "gitea.dev/models/actions"
	"gitea.dev/models/db"
	"gitea.dev/models/deployments/approvalgate"
	hub_model "gitea.dev/models/hub"
	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise JobIsHeldForReview itself — the exported function the spoke inside
// CreateTaskForRunner reaches through models/deployments/approvalgate — not only the pure
// ProjectReviewState helper underneath it. Testing the helper alone would leave the
// shipped path unverified, which is exactly how a fail-OPEN wrapper survives a green suite.

const (
	requesterID = int64(11)
	approverID  = int64(22)
	secondID    = int64(33)
)

// errUnexpectedLookup is what a stub returns from a branch the gate must never reach. The
// t.Fatal beside it is the real assertion; the error exists so the stub returns a value.
var errUnexpectedLookup = errors.New("the gate reached a lookup it should not have")

func approved(actor int64) Vote { return Vote{ActorID: actor, Event: AuditApproved} }
func rejected(actor int64) Vote { return Vote{ActorID: actor, Event: AuditRejected} }

// TestDeliveryProjectReviewStateCoversEveryPolicy exercises each policy in BOTH its
// accepting and its refusing case. A suite covering only the accepting path is treated as
// absent.
func TestDeliveryProjectReviewStateCoversEveryPolicy(t *testing.T) {
	cases := []struct {
		name      string
		policy    string
		required  int64
		votes     []Vote
		wantState string
		wantCount int64
	}{
		{"none never gates", PolicyNone, 1, nil, ReviewApproved, 0},
		{"none ignores an unapproved deploy", PolicyNone, 5, nil, ReviewApproved, 0},
		{"any_approver refuses with no review", PolicyAnyApprover, 1, nil, ReviewPending, 0},
		{
			"any_approver accepts the requester's own review", PolicyAnyApprover, 1,
			[]Vote{approved(requesterID)},
			ReviewApproved, 1,
		},
		{
			"others_only refuses the requester's own review", PolicyOthersOnly, 1,
			[]Vote{approved(requesterID)},
			ReviewPending, 0,
		},
		{
			"others_only accepts a second user's review", PolicyOthersOnly, 1,
			[]Vote{approved(requesterID), approved(approverID)},
			ReviewApproved, 1,
		},
		{
			"required 2 refuses after the first review", PolicyAnyApprover, 2,
			[]Vote{approved(approverID)},
			ReviewPending, 1,
		},
		{
			"required 2 accepts after the second", PolicyAnyApprover, 2,
			[]Vote{approved(approverID), approved(secondID)},
			ReviewApproved, 2,
		},
		{
			"required 2 does not count one approver twice", PolicyAnyApprover, 2,
			[]Vote{approved(approverID), approved(approverID)},
			ReviewPending, 1,
		},
		{
			"a rejection ends the deploy", PolicyAnyApprover, 1,
			[]Vote{rejected(approverID)},
			ReviewRejected, 0,
		},
		{
			"a later review does not revive a rejected deploy", PolicyAnyApprover, 1,
			[]Vote{rejected(approverID), approved(secondID)},
			ReviewRejected, 0,
		},
		{
			"an anonymous review counts for nobody", PolicyAnyApprover, 1,
			[]Vote{{ActorID: 0, Event: AuditApproved}},
			ReviewPending, 0,
		},
		{"required below one is still one review", PolicyAnyApprover, 0, nil, ReviewPending, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state, count := ProjectReviewState(c.policy, c.required, requesterID, c.votes)
			assert.Equal(t, c.wantState, state)
			assert.Equal(t, c.wantCount, count)
		})
	}
}

// gateStub resolves a gated repository whose job declares environment prod under the given
// policy, with the given votes already cast.
func gateStub(policy string, required int64, votes []Vote) reviewDeps {
	return reviewDeps{
		repoIsGated: func(context.Context, int64) (bool, error) { return true, nil },
		loadJob: func(context.Context, int64, int64) (*actions_model.ActionRunJob, error) {
			return &actions_model.ActionRunJob{ID: 5, RunID: 9, RepoID: 7}, nil
		},
		environment: func(context.Context, *actions_model.ActionRunJob) (string, error) { return "prod", nil },
		environmentRecord: func(context.Context, int64, string) (*Environment, error) {
			return &Environment{RepoID: 7, Name: "prod", ReviewPolicy: policy, RequiredReviewers: required}, nil
		},
		hold: func(context.Context, *actions_model.ActionRunJob, string) (*Review, error) {
			return &Review{ID: 1, RepoID: 7, RunID: 9, JobID: 5, Environment: "prod", RequesterID: requesterID}, nil
		},
		votes: func(context.Context, *Review) ([]Vote, error) { return votes, nil },
	}
}

// withGateDeps swaps the gate's lookups for the duration of one test.
func withGateDeps(t *testing.T, deps reviewDeps) {
	t.Helper()
	previous := reviewGateDeps
	reviewGateDeps = deps
	t.Cleanup(func() { reviewGateDeps = previous })
}

// TestDeliveryJobIsHeldForReviewHoldsAndReleases drives the exported gate itself for each
// policy, accepting and refusing.
func TestDeliveryJobIsHeldForReviewHoldsAndReleases(t *testing.T) {
	cases := []struct {
		name     string
		policy   string
		required int64
		votes    []Vote
		wantHeld bool
	}{
		{"none does not gate", PolicyNone, 1, nil, false},
		{"any_approver holds an unapproved deploy", PolicyAnyApprover, 1, nil, true},
		{
			"any_approver releases on the requester's own review", PolicyAnyApprover, 1,
			[]Vote{approved(requesterID)},
			false,
		},
		{
			"others_only holds on the requester's own review", PolicyOthersOnly, 1,
			[]Vote{approved(requesterID)},
			true,
		},
		{
			"others_only releases on someone else's", PolicyOthersOnly, 1,
			[]Vote{approved(approverID)},
			false,
		},
		{
			"required 2 stays held after the first", PolicyAnyApprover, 2,
			[]Vote{approved(approverID)},
			true,
		},
		{
			"required 2 releases after the second", PolicyAnyApprover, 2,
			[]Vote{approved(approverID), approved(secondID)},
			false,
		},
		{
			"a rejected deploy is never assigned", PolicyAnyApprover, 1,
			[]Vote{rejected(approverID)},
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withGateDeps(t, gateStub(c.policy, c.required, c.votes))
			assert.Equal(t, c.wantHeld, JobIsHeldForReview(t.Context(), 7, 5))
		})
	}
}

// TestDeliveryJobIsHeldForReviewFailsClosed is the security property: every lookup that
// cannot answer holds the job. An unassigned job is retried on the next poll; a production
// deploy that ran without its review cannot be taken back.
func TestDeliveryJobIsHeldForReviewFailsClosed(t *testing.T) {
	boom := errors.New("database unreachable")

	t.Run("the gated-repository query fails", func(t *testing.T) {
		deps := gateStub(PolicyAnyApprover, 1, nil)
		deps.repoIsGated = func(context.Context, int64) (bool, error) { return false, boom }
		withGateDeps(t, deps)
		assert.True(t, JobIsHeldForReview(t.Context(), 7, 5))
	})
	t.Run("the job cannot be loaded", func(t *testing.T) {
		deps := gateStub(PolicyAnyApprover, 1, nil)
		deps.loadJob = func(context.Context, int64, int64) (*actions_model.ActionRunJob, error) { return nil, boom }
		withGateDeps(t, deps)
		assert.True(t, JobIsHeldForReview(t.Context(), 7, 5))
	})
	t.Run("the environment cannot be read from the workflow", func(t *testing.T) {
		deps := gateStub(PolicyAnyApprover, 1, nil)
		deps.environment = func(context.Context, *actions_model.ActionRunJob) (string, error) { return "", boom }
		withGateDeps(t, deps)
		assert.True(t, JobIsHeldForReview(t.Context(), 7, 5))
	})
	t.Run("the environment record cannot be read", func(t *testing.T) {
		deps := gateStub(PolicyAnyApprover, 1, nil)
		deps.environmentRecord = func(context.Context, int64, string) (*Environment, error) { return nil, boom }
		withGateDeps(t, deps)
		assert.True(t, JobIsHeldForReview(t.Context(), 7, 5))
	})
	t.Run("the hold cannot be recorded", func(t *testing.T) {
		deps := gateStub(PolicyAnyApprover, 1, nil)
		deps.hold = func(context.Context, *actions_model.ActionRunJob, string) (*Review, error) { return nil, boom }
		withGateDeps(t, deps)
		assert.True(t, JobIsHeldForReview(t.Context(), 7, 5))
	})
	t.Run("the votes cannot be read", func(t *testing.T) {
		deps := gateStub(PolicyAnyApprover, 1, nil)
		deps.votes = func(context.Context, *Review) ([]Vote, error) { return nil, boom }
		withGateDeps(t, deps)
		assert.True(t, JobIsHeldForReview(t.Context(), 7, 5))
	})
}

// TestDeliveryJobIsHeldForReviewLeavesUngatedJobsAlone is the "adding the fork changes no
// behaviour" property: a repository with no gated environment, and a job that
// declares no environment, are both assigned without a workflow read.
func TestDeliveryJobIsHeldForReviewLeavesUngatedJobsAlone(t *testing.T) {
	t.Run("no environment of the repository is gated", func(t *testing.T) {
		deps := gateStub(PolicyAnyApprover, 1, nil)
		deps.repoIsGated = func(context.Context, int64) (bool, error) { return false, nil }
		deps.loadJob = func(context.Context, int64, int64) (*actions_model.ActionRunJob, error) {
			t.Fatal("an ungated repository must not cost a job load, let alone a workflow read")
			return nil, errUnexpectedLookup
		}
		withGateDeps(t, deps)
		assert.False(t, JobIsHeldForReview(t.Context(), 7, 5))
	})
	t.Run("the job declares no environment", func(t *testing.T) {
		deps := gateStub(PolicyAnyApprover, 1, nil)
		deps.environment = func(context.Context, *actions_model.ActionRunJob) (string, error) { return "", nil }
		deps.hold = func(context.Context, *actions_model.ActionRunJob, string) (*Review, error) {
			t.Fatal("a job with no environment must not be recorded as held")
			return nil, errUnexpectedLookup
		}
		withGateDeps(t, deps)
		assert.False(t, JobIsHeldForReview(t.Context(), 7, 5))
	})
}

// TestDeliveryReviewGateSeamIsWired is the fork-absent case, and the wiring that makes the
// spoke work: with nothing registered the dispatcher answers false, and models/deployments'
// own init has registered the real gate.
func TestDeliveryReviewGateSeamIsWired(t *testing.T) {
	withGateDeps(t, gateStub(PolicyAnyApprover, 1, nil))
	assert.True(t, approvalgate.Held(t.Context(), 7, 5),
		"models/deployments' init must have registered the gate, or the spoke calls nothing")

	approvalgate.Register(nil)
	t.Cleanup(func() { approvalgate.Register(JobIsHeldForReview) })
	assert.False(t, approvalgate.Held(t.Context(), 7, 5),
		"with the fork absent the dispatcher claims jobs exactly as stock Gitea does")
}

// TestDeliveryReviewTableIsAppendOnly holds the same guarantee deployments and audit hold:
// a row carrying a primary key is an update written through the model, and it is refused.
func TestDeliveryReviewTableIsAppendOnly(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	hold := &Review{RepoID: 7, Environment: "PROD", RunID: 9, JobID: 5, RequesterID: requesterID, RequesterLogin: "user2"}
	require.NoError(t, AppendReview(t.Context(), hold))
	require.NotZero(t, hold.ID)
	assert.Equal(t, "prod", hold.Environment, "environment names are identifiers and are stored lower-cased")

	err := AppendReview(t.Context(), hold)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "append-only")

	read, err := GetReviewByID(t.Context(), hold.ID)
	require.NoError(t, err)
	assert.Equal(t, hold.RunID, read.RunID)

	_, err = GetReviewByID(t.Context(), hold.ID+9999)
	require.Error(t, err)
	var hubErr *hub_model.Error
	require.ErrorAs(t, err, &hubErr)
	assert.NotEmpty(t, hubErr.SuggestedAction, "every error carries a suggested next action")

	// A hold naming no run cannot release anything, so it is refused rather than stored.
	require.Error(t, AppendReview(t.Context(), &Review{RepoID: 7, Environment: "prod"}))
	require.Error(t, AppendReview(t.Context(), &Review{RepoID: 0, Environment: "prod", RunID: 1, JobID: 1}))
	require.Error(t, AppendReview(t.Context(), &Review{RepoID: 7, Environment: "  ", RunID: 1, JobID: 1}))
}

// TestDeliveryRepoHasGatedEnvironmentIsTheFastPath covers the one query every ordinary job
// pays for. It has to answer false for a repository whose environments are all `none`, or
// the fork would hold every job in the instance.
func TestDeliveryRepoHasGatedEnvironmentIsTheFastPath(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	const repoID = int64(4242)
	gated, err := RepoHasGatedEnvironment(t.Context(), repoID)
	require.NoError(t, err)
	assert.False(t, gated, "a repository with no environment of its own and ungated defaults is not gated")

	require.NoError(t, db.Insert(t.Context(), &Environment{
		RepoID: repoID, Name: "prod", ReviewPolicy: PolicyNone, RequiredReviewers: 1,
	}))
	gated, err = RepoHasGatedEnvironment(t.Context(), repoID)
	require.NoError(t, err)
	assert.False(t, gated, "an environment whose policy is none gates nothing")

	require.NoError(t, db.Insert(t.Context(), &Environment{
		RepoID: repoID, Name: "staging", ReviewPolicy: PolicyOthersOnly, RequiredReviewers: 1,
	}))
	gated, err = RepoHasGatedEnvironment(t.Context(), repoID)
	require.NoError(t, err)
	assert.True(t, gated)
}

// TestDeliveryVotesForReviewReadsTheAuditLog proves reviews are not stored a second
// time: the gate's inputs are the audit rows the approve and reject endpoints append.
func TestDeliveryVotesForReviewReadsTheAuditLog(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	hold := &Review{RepoID: 91, Environment: "prod", RunID: 77, JobID: 5, RequesterID: requesterID}
	require.NoError(t, AppendReview(t.Context(), hold))

	for _, e := range []*AuditEvent{
		{Event: AuditRequested, RepoID: 91, Environment: "prod", RunID: 77, Source: SourceNotifier},
		{Event: AuditApproved, RepoID: 91, Environment: "prod", RunID: 77, ActorID: approverID, ActorLogin: "user2", Source: SourceUI},
		{Event: AuditApproved, RepoID: 91, Environment: "qa", RunID: 77, ActorID: secondID, Source: SourceUI},
	} {
		require.NoError(t, AppendAuditEvent(t.Context(), e))
	}

	votes, err := VotesForReview(t.Context(), hold)
	require.NoError(t, err)
	require.Len(t, votes, 1, "only reviews and rejections of THIS environment's run count")
	assert.Equal(t, approverID, votes[0].ActorID)
	assert.Equal(t, AuditApproved, votes[0].Event)
}

// TestDeliveryReleaseTagOfRef covers what the hold records about a deploy dispatched at a
// tag and one dispatched at a branch, which carries no release identity.
func TestDeliveryReleaseTagOfRef(t *testing.T) {
	assert.Equal(t, "v1.2.3", releaseTagOfRef("refs/tags/v1.2.3"))
	assert.Empty(t, releaseTagOfRef("refs/heads/main"))
	assert.Empty(t, releaseTagOfRef(""))
}
