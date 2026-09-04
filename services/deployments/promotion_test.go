// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"testing"

	deployments_model "gitea.dev/models/deployments"

	"github.com/stretchr/testify/assert"
)

func TestDeploymentsWorkflowIDForEnvironment(t *testing.T) {
	assert.Equal(t, "deploy-prod.yaml", WorkflowIDForEnvironment("PROD"))
	assert.Equal(t, "deploy-qa.yaml", WorkflowIDForEnvironment("  qa "))
	assert.Empty(t, WorkflowIDForEnvironment("  "), "an unnamed environment names no workflow")

	// It is the inverse of the notifier's reader, so a run this dispatches is recorded
	// against the environment it was dispatched for. One convention, read both ways.
	for _, name := range []string{"dev", "qa", "uat", "staging", "prod"} {
		assert.Equal(t, name, EnvironmentFromWorkflowID(WorkflowIDForEnvironment(name)))
	}
}

// TestDeploymentsAcceptsRelease is the offer rule, which reads one column and no name: an
// environment takes anything unless it has asked for finished releases only. Both
// directions per environment.
func TestDeploymentsAcceptsRelease(t *testing.T) {
	open := &deployments_model.Environment{Name: "anything-at-all"}
	assert.True(t, AcceptsRelease(open, true), "an environment offers prereleases by default")
	assert.True(t, AcceptsRelease(open, false), "and full releases too")

	closed := &deployments_model.Environment{Name: "anything-at-all", ReleasesOnly: true}
	assert.False(t, AcceptsRelease(closed, true), "releases_only refuses a prerelease")
	assert.True(t, AcceptsRelease(closed, false), "it still takes a full release")

	assert.True(t, AcceptsRelease(nil, true), "a missing environment refuses nothing here; the caller has already 404ed")
}

// succeeded builds one success of tag in environment at t.
func succeeded(id int64, environment, tag string, at int64) Event {
	return Event{ID: id, Environment: environment, ReleaseTag: tag, Event: deployments_model.AuditSucceeded, OccurredUnix: at}
}

// TestDeploymentsEvaluateDependencies covers the three states the sequence rule distinguishes
// for a single dependency: never held, held previously, and currently live.
func TestDeploymentsEvaluateDependencies(t *testing.T) {
	dep, state := EvaluateDependencies(nil, "v1.0", nil)
	assert.Empty(t, dep)
	assert.Equal(t, PredecessorNone, state, "an environment that declares no dependency has no sequence to check")

	dep, state = EvaluateDependencies([]string{""}, "v1.0", nil)
	assert.Empty(t, dep)
	assert.Equal(t, PredecessorNone, state, "a depends_on holding only an empty name is the same as none")

	dep, state = EvaluateDependencies([]string{"staging"}, "v1.0", nil)
	assert.Equal(t, "staging", dep)
	assert.Equal(t, PredecessorNever, state)

	dep, state = EvaluateDependencies([]string{"staging"}, "v1.0", []Event{
		succeeded(1, "qa", "v1.0", 100),
		succeeded(2, "staging", "v0.9", 200),
	})
	assert.Equal(t, "staging", dep)
	assert.Equal(t, PredecessorNever, state, "a success of a different release, or in a different environment, is not this release having held")

	dep, state = EvaluateDependencies([]string{"staging"}, "v1.0", []Event{
		succeeded(1, "staging", "v1.0", 100),
	})
	assert.Equal(t, "staging", dep)
	assert.Equal(t, PredecessorLive, state)

	dep, state = EvaluateDependencies([]string{"staging"}, "v1.0", []Event{
		succeeded(1, "staging", "v1.0", 100),
		succeeded(2, "staging", "v1.1", 200),
	})
	assert.Equal(t, "staging", dep)
	assert.Equal(t, PredecessorHeld, state, "v1.0 held staging and v1.1 replaced it; the sequence was still satisfied")

	dep, state = EvaluateDependencies([]string{"STAGING"}, "v1.0", []Event{
		succeeded(1, "staging", "v1.0", 100),
		{ID: 2, Environment: "staging", ReleaseTag: "v1.0", Event: deployments_model.AuditFailed, OccurredUnix: 200},
	})
	assert.Equal(t, "staging", dep)
	assert.Equal(t, PredecessorHeld, state, "a later failure does not unmake the success that already happened")
}

// TestDeploymentsEvaluateDependenciesRequiresEveryOne proves the generalisation: with several
// dependencies every one of them must hold the release, and the first that has not is the
// one named.
func TestDeploymentsEvaluateDependenciesRequiresEveryOne(t *testing.T) {
	events := []Event{
		succeeded(1, "qa", "v1.0", 100),
	}
	dep, state := EvaluateDependencies([]string{"qa", "staging"}, "v1.0", events)
	assert.Equal(t, "staging", dep, "qa held it; staging is the first that has not")
	assert.Equal(t, PredecessorNever, state)

	both := append(append([]Event{}, events...), succeeded(2, "staging", "v1.0", 200))
	dep, state = EvaluateDependencies([]string{"qa", "staging"}, "v1.0", both)
	assert.Equal(t, "staging", dep, "once every dependency has held it, the last one is reported")
	assert.Equal(t, PredecessorLive, state, "v1.0 is the only success staging has had, so it is still live there")
}

// TestDeploymentsDecidePromotion covers the sequence-rule table, every row, in its accepting
// and its refusing case.
func TestDeploymentsDecidePromotion(t *testing.T) {
	warnOnly := &deployments_model.Environment{Name: "prod", DependsOn: []string{"staging"}}
	gated := &deployments_model.Environment{Name: "prod", DependsOn: []string{"staging"}, RequirePriorDeployment: true}

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
		{name: "no dependency declared — nothing to decide", env: &deployments_model.Environment{Name: "dev"}, state: PredecessorNone, want: OutcomeProceed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := DecidePromotion(tc.env, "staging", tc.state, tc.canBypass)
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
