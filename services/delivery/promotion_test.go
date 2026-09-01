// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"testing"

	delivery_model "gitea.dev/models/delivery"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeliveryWorkflowIDForEnvironment(t *testing.T) {
	assert.Equal(t, "deploy-prod.yaml", WorkflowIDForEnvironment("PROD"))
	assert.Equal(t, "deploy-qa.yaml", WorkflowIDForEnvironment("  qa "))
	assert.Empty(t, WorkflowIDForEnvironment("  "), "an unnamed environment names no workflow")

	// It is the inverse of the notifier's reader, so a run this dispatches is recorded
	// against the environment it was dispatched for. One convention, read both ways (D4).
	for _, name := range []string{"dev", "qa", "uat", "staging", "prod"} {
		assert.Equal(t, name, EnvironmentFromWorkflowID(WorkflowIDForEnvironment(name)))
	}
}

// TestDeliveryAcceptsRelease is the offer rule: prereleases reach dev, qa and uat only, and
// full releases reach every environment. Both directions per environment (J9).
func TestDeliveryAcceptsRelease(t *testing.T) {
	accepting := DefaultPrereleaseEnvironments

	for _, name := range []string{"dev", "qa", "uat"} {
		assert.True(t, AcceptsRelease(name, true, accepting), "%s is offered prereleases", name)
		assert.True(t, AcceptsRelease(name, false, accepting), "%s is offered full releases too", name)
	}
	for _, name := range []string{"staging", "prod"} {
		assert.False(t, AcceptsRelease(name, true, accepting), "%s is not offered prereleases", name)
		assert.True(t, AcceptsRelease(name, false, accepting), "%s is offered full releases", name)
	}
	assert.True(t, AcceptsRelease("QA", true, accepting), "the environment name is normalized before it is matched")
	assert.True(t, AcceptsRelease("qa", true, []string{" QA "}), "so is the configured set")
	assert.False(t, AcceptsRelease("dev", true, nil), "an empty set offers a prerelease nowhere")
}

// succeeded builds one success of tag in environment at t.
func succeeded(id int64, environment, tag string, at int64) Event {
	return Event{ID: id, Environment: environment, ReleaseTag: tag, Event: delivery_model.AuditSucceeded, OccurredUnix: at}
}

// TestDeliveryEvaluatePredecessor covers the three states E17 distinguishes: never held,
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
		{ID: 2, Environment: "staging", ReleaseTag: "v1.0", Event: delivery_model.AuditFailed, OccurredUnix: 200},
	}), "a later failure does not unmake the success that already happened")
}

// TestDeliveryDecidePromotion is E17's table, every row, in its accepting and its refusing
// case (J9, SC 40).
func TestDeliveryDecidePromotion(t *testing.T) {
	warnOnly := &delivery_model.Environment{Name: "prod", Predecessor: "staging"}
	gated := &delivery_model.Environment{Name: "prod", Predecessor: "staging", RequirePredecessor: true}

	cases := []struct {
		name       string
		env        *delivery_model.Environment
		state      PredecessorState
		canBypass  bool
		want       Outcome
		needReason bool
	}{
		{name: "flag off, never held, cannot bypass — warned only (F11)", env: warnOnly, state: PredecessorNever, want: OutcomeWarn},
		{name: "flag off, never held, can bypass — still only a warning", env: warnOnly, state: PredecessorNever, canBypass: true, want: OutcomeWarn},
		{name: "flag off, held — nothing to warn about", env: warnOnly, state: PredecessorHeld, want: OutcomeProceed},
		{name: "flag on, held — proceeds", env: gated, state: PredecessorHeld, want: OutcomeProceed},
		{name: "flag on, live — proceeds", env: gated, state: PredecessorLive, want: OutcomeProceed},
		{name: "flag on, held, can bypass — proceeds without needing a reason", env: gated, state: PredecessorHeld, canBypass: true, want: OutcomeProceed},
		{name: "flag on, never held, cannot bypass — refused", env: gated, state: PredecessorNever, want: OutcomeRefuse},
		{name: "flag on, never held, can bypass — offered as an override with a reason", env: gated, state: PredecessorNever, canBypass: true, want: OutcomeOverride, needReason: true},
		{name: "no predecessor declared — nothing to decide", env: &delivery_model.Environment{Name: "dev"}, state: PredecessorNone, want: OutcomeProceed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := DecidePromotion(tc.env, tc.state, tc.canBypass)
			assert.Equal(t, tc.want, d.Outcome)
			assert.Equal(t, tc.needReason, d.RequiresOverrideReason)
			assert.Equal(t, tc.state, d.PredecessorState)
			if tc.want != OutcomeProceed {
				assert.NotEmpty(t, d.Message)
				assert.NotEmpty(t, d.SuggestedAction, "every decision carries a suggested next action (A21)")
			}
		})
	}
}

func TestDeliveryPrereleaseEnvironmentsDefaultsWithoutConfiguration(t *testing.T) {
	// setting.CfgProvider is nil in this package's unit run, which is the "no [delivery]
	// section at all" case: the fork behaves the same whether or not one exists.
	require.Equal(t, []string{"dev", "qa", "uat"}, PrereleaseEnvironments())
}
