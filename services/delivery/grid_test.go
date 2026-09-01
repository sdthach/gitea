// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"testing"

	delivery_model "gitea.dev/models/delivery"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// event is a shorthand for one audit row in the projection's input.
func event(at int64, release, environment, name string) Event {
	return Event{ID: at, OccurredUnix: at, ReleaseTag: release, Environment: environment, Event: name, RunID: at}
}

// TestDeliveryGridCellStatePerEventSequence is J5's table: one cell state per event
// sequence, including ⏸ and ✔ ×N. Every state is projected; none is stored.
func TestDeliveryGridCellStatePerEventSequence(t *testing.T) {
	cases := []struct {
		name     string
		events   []Event
		policy   string
		state    CellState
		symbol   string
		expected int
	}{
		{
			name: "no event at all", events: nil,
			state: CellNever, symbol: "·",
		},
		{
			name:   "requested into an ungated environment is queued, not held",
			events: []Event{event(1, "v1", "qa", delivery_model.AuditRequested)},
			policy: delivery_model.PolicyNone,
			state:  CellInProgress, symbol: "⟳",
		},
		{
			name:   "requested into a gated environment is held",
			events: []Event{event(1, "v1", "qa", delivery_model.AuditRequested)},
			policy: delivery_model.PolicyAnyApprover,
			state:  CellHeld, symbol: "⏸",
		},
		{
			name: "approved but not yet started is on its way, not held",
			events: []Event{
				event(1, "v1", "qa", delivery_model.AuditRequested),
				event(2, "v1", "qa", delivery_model.AuditApproved),
			},
			policy: delivery_model.PolicyAnyApprover,
			state:  CellInProgress, symbol: "⟳",
		},
		{
			name: "rejected reads as a failed attempt",
			events: []Event{
				event(1, "v1", "qa", delivery_model.AuditRequested),
				event(2, "v1", "qa", delivery_model.AuditRejected),
			},
			policy: delivery_model.PolicyOthersOnly,
			state:  CellFailed, symbol: "✗",
		},
		{
			name: "started",
			events: []Event{
				event(1, "v1", "qa", delivery_model.AuditRequested),
				event(2, "v1", "qa", delivery_model.AuditStarted),
			},
			state: CellInProgress, symbol: "⟳",
		},
		{
			name: "succeeded once and still live",
			events: []Event{
				event(1, "v1", "qa", delivery_model.AuditStarted),
				event(2, "v1", "qa", delivery_model.AuditSucceeded),
			},
			state: CellLive, symbol: "✔ now", expected: 1,
		},
		{
			name: "succeeded twice and still live",
			events: []Event{
				event(1, "v1", "qa", delivery_model.AuditSucceeded),
				event(2, "v1", "qa", delivery_model.AuditSucceeded),
			},
			state: CellLive, symbol: "✔ ×2 now", expected: 2,
		},
		{
			name: "the last attempt failed, whatever came before it",
			events: []Event{
				event(1, "v1", "qa", delivery_model.AuditSucceeded),
				event(2, "v1", "qa", delivery_model.AuditFailed),
			},
			state: CellFailed, symbol: "✗", expected: 1,
		},
		{
			name: "a cancelled deploy is a failed attempt",
			events: []Event{
				event(1, "v1", "qa", delivery_model.AuditStarted),
				event(2, "v1", "qa", delivery_model.AuditCancelled),
			},
			state: CellFailed, symbol: "✗",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cells := ProjectCells([]string{"qa"}, []string{"v1"}, tc.events, map[string]string{"qa": tc.policy})
			require.Len(t, cells["v1"], 1)
			cell := cells["v1"][0]
			assert.Equal(t, tc.state, cell.State)
			assert.Equal(t, tc.symbol, cell.Symbol)
			assert.Equal(t, tc.expected, cell.Successes)
		})
	}
}

// TestDeliveryGridProjectsSC14 is SC 14 exactly: v1 to qa, v2 to qa, v1 to qa.
func TestDeliveryGridProjectsSC14(t *testing.T) {
	events := []Event{
		event(10, "v1", "qa", delivery_model.AuditSucceeded),
		event(20, "v2", "qa", delivery_model.AuditSucceeded),
		event(30, "v1", "qa", delivery_model.AuditSucceeded),
	}
	cells := ProjectCells([]string{"qa"}, []string{"v1", "v2"}, events, nil)

	v1, v2 := cells["v1"][0], cells["v2"][0]
	assert.Equal(t, CellLive, v1.State, "v1 was deployed last, so v1 is what qa is holding")
	assert.Equal(t, "✔ ×2 now", v1.Symbol)
	assert.Equal(t, 2, v1.Successes)

	assert.Equal(t, CellSuperseded, v2.State, "v2 reached qa but no longer holds it")
	assert.Equal(t, "✔", v2.Symbol)
	assert.Equal(t, 1, v2.Successes)
}

func TestDeliveryGridColumnsFollowConfiguredOrder(t *testing.T) {
	events := []Event{event(1, "v1", "prod", delivery_model.AuditSucceeded)}
	cells := ProjectCells([]string{"dev", "qa", "prod"}, []string{"v1"}, events, nil)

	require.Len(t, cells["v1"], 3)
	assert.Equal(t, []string{"dev", "qa", "prod"},
		[]string{cells["v1"][0].Environment, cells["v1"][1].Environment, cells["v1"][2].Environment},
		"environment sequence is configuration; nothing in Gitea expresses it (E7)")
	assert.Equal(t, "·", cells["v1"][0].Symbol)
	assert.Equal(t, "·", cells["v1"][1].Symbol)
	assert.Equal(t, "✔ now", cells["v1"][2].Symbol)
}

func TestDeliveryGridEnvironmentsAreIndependent(t *testing.T) {
	events := []Event{
		event(10, "v1", "qa", delivery_model.AuditSucceeded),
		event(20, "v2", "qa", delivery_model.AuditSucceeded),
		event(30, "v1", "prod", delivery_model.AuditSucceeded),
	}
	cells := ProjectCells([]string{"qa", "prod"}, []string{"v1", "v2"}, events, nil)

	assert.Equal(t, CellSuperseded, cells["v1"][0].State, "qa moved on to v2")
	assert.Equal(t, CellLive, cells["v1"][1].State, "prod is still on v1")
	assert.Equal(t, CellLive, cells["v2"][0].State, "qa holds v2")
	assert.Equal(t, CellNever, cells["v2"][1].State, "v2 never reached prod")
	assert.Equal(t, "·", cells["v2"][1].Symbol)
}

// TestDeliveryGridOrdersOutOfOrderEvents covers the case the notifier can produce: events
// arriving with the same timestamp, or later ones written first.
func TestDeliveryGridOrdersOutOfOrderEvents(t *testing.T) {
	events := []Event{
		{ID: 2, OccurredUnix: 100, ReleaseTag: "v2", Environment: "qa", Event: delivery_model.AuditSucceeded},
		{ID: 1, OccurredUnix: 100, ReleaseTag: "v1", Environment: "qa", Event: delivery_model.AuditSucceeded},
	}
	cells := ProjectCells([]string{"qa"}, []string{"v1", "v2"}, events, nil)

	assert.Equal(t, CellLive, cells["v2"][0].State, "the tie is broken on the primary key, so the later row wins")
	assert.Equal(t, CellSuperseded, cells["v1"][0].State)
}

func TestDeliveryGridCarriesTheRunLink(t *testing.T) {
	events := []Event{{
		ID: 1, OccurredUnix: 5, ReleaseTag: "v1", Environment: "qa",
		Event: delivery_model.AuditSucceeded, RunID: 77, RunURL: "https://gitea.example.com/o/r/actions/runs/77",
	}}
	cell := ProjectCells([]string{"qa"}, []string{"v1"}, events, nil)["v1"][0]

	assert.Equal(t, int64(77), cell.RunID, "a cell opens its run (E8)")
	assert.Equal(t, "https://gitea.example.com/o/r/actions/runs/77", cell.RunURL)
	assert.Equal(t, int64(5), cell.OccurredUnix)
}
