// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"testing"

	deployments_model "gitea.dev/models/deployments"

	"github.com/stretchr/testify/assert"
)

func TestDeliveryWorkflowIDForEnvironment(t *testing.T) {
	assert.Equal(t, "deploy-prod.yaml", WorkflowIDForEnvironment("PROD"))
	assert.Equal(t, "deploy-qa.yaml", WorkflowIDForEnvironment("  qa "))
	assert.Empty(t, WorkflowIDForEnvironment("  "), "an unnamed environment names no workflow")

	// It is the inverse of the notifier's reader, so a run this dispatches is recorded
	// against the environment it was dispatched for. One convention, read both ways.
	for _, name := range []string{"dev", "qa", "uat", "staging", "prod"} {
		assert.Equal(t, name, EnvironmentFromWorkflowID(WorkflowIDForEnvironment(name)))
	}
}

// TestDeliveryAcceptsRelease is the offer rule, which reads one column and no name: an
// environment takes anything unless it has asked for finished releases only. Both
// directions per environment.
func TestDeliveryAcceptsRelease(t *testing.T) {
	open := &deployments_model.Environment{Name: "anything-at-all"}
	assert.True(t, AcceptsRelease(open, true), "an environment offers prereleases by default")
	assert.True(t, AcceptsRelease(open, false), "and full releases too")

	closed := &deployments_model.Environment{Name: "anything-at-all", RequireFullRelease: true}
	assert.False(t, AcceptsRelease(closed, true), "require_full_release refuses a prerelease")
	assert.True(t, AcceptsRelease(closed, false), "it still takes a full release")

	assert.True(t, AcceptsRelease(nil, true), "a missing environment refuses nothing here; the caller has already 404ed")
}

// succeeded builds one success of tag in environment at t.
func succeeded(id int64, environment, tag string, at int64) Event {
	return Event{ID: id, Environment: environment, ReleaseTag: tag, Event: deployments_model.AuditSucceeded, OccurredUnix: at}
}

// TestDeliveryEvaluatePredecessor covers the three states the sequence rule distinguishes: never held,
// held previously, and currently live.
func TestDeliveryEvaluatePredecessor(t *testing.T) {
	assert.Equal(t, PredecessorNone, EvaluatePredecessor("", "v1.0", nil),
		"an environment that declares no predecessor has no sequence to check")

	assert.Equal(t, PredecessorNever, EvaluatePredecessor("staging", "v1.0", nil))
	assert.Equal(t, PredecessorNever, EvaluatePredecessor("staging", "v1.0", []Event{
		succeeded(1, "qa", "v1.0", 100),
		succeeded(2, "staging", "v0.9", 200),
	}), "a success of a different release, or in a different environment, is not this release having held")

	assert.Equal(t, PredecessorLive, EvaluatePredecessor("staging", "v1.0", []Event{
		succeeded(1, "staging", "v1.0", 100),
	}))

	assert.Equal(t, PredecessorHeld, EvaluatePredecessor("staging", "v1.0", []Event{
		succeeded(1, "staging", "v1.0", 100),
		succeeded(2, "staging", "v1.1", 200),
	}), "v1.0 held staging and v1.1 replaced it; the sequence was still satisfied")

	assert.Equal(t, PredecessorHeld, EvaluatePredecessor("STAGING", "v1.0", []Event{
		succeeded(1, "staging", "v1.0", 100),
		{ID: 2, Environment: "staging", ReleaseTag: "v1.0", Event: deployments_model.AuditFailed, OccurredUnix: 200},
	}), "a later failure does not unmake the success that already happened")
}

// TestDeliveryDecidePromotion covers the sequence-rule table, every row, in its accepting
// and its refusing case.
func TestDeliveryDecidePromotion(t *testing.T) {
	warnOnly := &deployments_model.Environment{Name: "prod", Predecessor: "staging"}
	gated := &deployments_model.Environment{Name: "prod", Predecessor: "staging", RequirePredecessor: true}

	cases := []struct {
		name       string
		env        *deployments_model.Environment
		state      PredecessorState
		canBypass  bool
		want       Outcome
		needReason bool
	}{
		{name: "flag off, never held, cannot bypass — warned only", env: warnOnly, state: PredecessorNever, want: OutcomeWarn},
		{name: "flag off, never held, can bypass — still only a warning", env: warnOnly, state: PredecessorNever, canBypass: true, want: OutcomeWarn},
		{name: "flag off, held — nothing to warn about", env: warnOnly, state: PredecessorHeld, want: OutcomeProceed},
		{name: "flag on, held — proceeds", env: gated, state: PredecessorHeld, want: OutcomeProceed},
		{name: "flag on, live — proceeds", env: gated, state: PredecessorLive, want: OutcomeProceed},
		{name: "flag on, held, can bypass — proceeds without needing a reason", env: gated, state: PredecessorHeld, canBypass: true, want: OutcomeProceed},
		{name: "flag on, never held, cannot bypass — refused", env: gated, state: PredecessorNever, want: OutcomeRefuse},
		{name: "flag on, never held, can bypass — offered as an override with a reason", env: gated, state: PredecessorNever, canBypass: true, want: OutcomeOverride, needReason: true},
		{name: "no predecessor declared — nothing to decide", env: &deployments_model.Environment{Name: "dev"}, state: PredecessorNone, want: OutcomeProceed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := DecidePromotion(tc.env, tc.state, tc.canBypass)
			assert.Equal(t, tc.want, d.Outcome)
			assert.Equal(t, tc.needReason, d.RequiresOverrideReason)
			assert.Equal(t, tc.state, d.PredecessorState)
			if tc.want != OutcomeProceed {
				assert.NotEmpty(t, d.Message)
				assert.NotEmpty(t, d.SuggestedAction, "every decision carries a suggested next action")
			}
		})
	}
}
