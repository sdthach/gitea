// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"testing"

	hub_model "gitea.dev/models/hub"
	"gitea.dev/models/unittest"
	"gitea.dev/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeploymentsAuditEnumsAreDeclaredOnce(t *testing.T) {
	assert.Equal(t,
		[]string{
			"requested", "started", "succeeded", "failed", "cancelled", "approved", "rejected", "overridden",
			"checks_pending", "checks_passed", "checks_failed", "auto_promoted",
		},
		AuditEvents, "the event set is declared once, in order, with override appended rather than inserted")
	assert.Equal(t, []string{"ui", "notifier", "reconcile"}, AuditSources)
}

func TestDeploymentsValidateAuditEvent(t *testing.T) {
	valid := func() *AuditEvent {
		return &AuditEvent{Event: AuditSucceeded, Source: SourceNotifier, RepoID: 1, Environment: "prod"}
	}
	require.NoError(t, ValidateAuditEvent(valid()))

	cases := map[string]func(*AuditEvent){
		"unknown event":  func(e *AuditEvent) { e.Event = "deployed" },
		"unknown source": func(e *AuditEvent) { e.Source = "webhook" },
		"no repository":  func(e *AuditEvent) { e.RepoID = 0 },
		"no environment": func(e *AuditEvent) { e.Environment = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := valid()
			mutate(e)
			err := ValidateAuditEvent(e)
			require.Error(t, err, "an unrecognised row would render as an unknown cell state instead of failing where it was written")

			var hubErr *hub_model.Error
			require.ErrorAs(t, err, &hubErr)
			assert.NotEmpty(t, hubErr.SuggestedAction, "every error carries a suggested next action")
		})
	}
}

// TestDeploymentsAuditIsAppendOnly: the log only grows, and it keeps
// naming the actor after the Gitea user is gone.
func TestDeploymentsAuditIsAppendOnly(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	requested := &AuditEvent{
		Event: AuditRequested, Source: SourceUI, RepoID: 1, Environment: "PROD", ReleaseTag: "v1",
		ActorID: 2, ActorLogin: "user2", RunID: 55, OccurredUnix: timeutil.TimeStamp(1000),
	}
	require.NoError(t, AppendAuditEvent(ctx, requested))
	require.NoError(t, AppendAuditEvent(ctx, &AuditEvent{
		Event: AuditSucceeded, Source: SourceNotifier, RepoID: 1, Environment: "prod", ReleaseTag: "v1",
		ActorID: 2, ActorLogin: "user2", RunID: 55, OccurredUnix: timeutil.TimeStamp(1010),
	}))

	rows, err := FindAuditEvents(ctx, builderEq("repo_id", int64(1)), "occurred_unix ASC, id ASC", 0)
	require.NoError(t, err)
	require.Len(t, rows, 2, "each deploy writes requested and a terminal event, so request-to-live latency is derivable")
	assert.Equal(t, "prod", rows[0].Environment, "environment names are identifiers, stored lower-cased")
	assert.Equal(t, int64(10), int64(rows[1].OccurredUnix-rows[0].OccurredUnix))

	// Re-saving a row is what an update looks like through the model. No row may be
	// updated or deleted.
	err = AppendAuditEvent(ctx, rows[0])
	require.Error(t, err)
	var hubErr *hub_model.Error
	require.ErrorAs(t, err, &hubErr)
	assert.Contains(t, hubErr.Message, "append-only")
	assert.NotEmpty(t, hubErr.SuggestedAction)

	still, err := FindAuditEvents(ctx, builderEq("repo_id", int64(1)), "id ASC", 0)
	require.NoError(t, err)
	assert.Len(t, still, 2, "the refused write left the log untouched")
}

func TestDeploymentsAuditStampsOccurredWhenTheCallerDoesNot(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	event := &AuditEvent{Event: AuditStarted, Source: SourceNotifier, RepoID: 3, Environment: "qa", ReleaseTag: "v9"}
	require.NoError(t, AppendAuditEvent(ctx, event))
	assert.NotZero(t, event.OccurredUnix, "an event with no time cannot be ordered, so one is stamped rather than left at zero")
}
