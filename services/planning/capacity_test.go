// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"testing"
	"time"

	hub_model "gitea.dev/models/hub"
	planning_model "gitea.dev/models/planning"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aMonday is a fixed Monday so every weekday-mask table below reads against a known calendar.
var aMonday = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC) // Monday

// TestShadowCapacitiesPrefersNearestScope is mutation proof for item 1: with a repo, an org and
// an instance row for the same user, the repo row wins; drop it and the org row wins; drop that
// too and the instance row is all that is left. Inverting nearness's ordering must fail this.
func TestShadowCapacitiesPrefersNearestScope(t *testing.T) {
	repoID, orgID := int64(7), int64(5)
	rows := []*planning_model.UserCapacity{
		{UserID: 1, RepoID: repoID, HoursPerDay: 6, Utilization: 0.6, Workdays: 62},
		{UserID: 1, OrgID: orgID, HoursPerDay: 7, Utilization: 0.7, Workdays: 62},
		{UserID: 1, HoursPerDay: 8, Utilization: 0.8, Workdays: 62},
	}

	out := shadowCapacities(rows, repoID, orgID, []int64{1})
	assert.Equal(t, CapacitySourceRepo, out[1].Source)
	assert.InEpsilon(t, 6, out[1].HoursPerDay, 0.001)

	rows = rows[1:] // repo row gone; org is nearest now
	out = shadowCapacities(rows, repoID, orgID, []int64{1})
	assert.Equal(t, CapacitySourceOrg, out[1].Source)
	assert.InEpsilon(t, 7, out[1].HoursPerDay, 0.001)

	rows = rows[1:] // org row gone too; only the instance row is left
	out = shadowCapacities(rows, repoID, orgID, []int64{1})
	assert.Equal(t, CapacitySourceInstance, out[1].Source)
	assert.InEpsilon(t, 8, out[1].HoursPerDay, 0.001)
}

func TestValidateCapacity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		hours, util float64
		workdays    int
		wantCode    string
	}{
		{"zero hours", 0, 0.8, 62, "bad_hours"},
		{"24 hours accepted", 24, 0.8, 62, ""},
		{"25 hours refused", 24.001, 0.8, 62, "bad_hours"},
		{"negative hours", -1, 0.8, 62, "bad_hours"},
		{"zero utilization", 8, 0, 62, "bad_utilization"},
		{"1.0 utilization accepted", 8, 1, 62, ""},
		{"over 1.0 utilization", 8, 1.01, 62, "bad_utilization"},
		{"workdays 0 refused", 8, 0.8, 0, "bad_workdays"},
		{"workdays 1 accepted", 8, 0.8, 1, ""},
		{"workdays 127 accepted", 8, 0.8, 127, ""},
		{"workdays 128 refused", 8, 0.8, 128, "bad_workdays"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCapacity(tc.hours, tc.util, tc.workdays)
			if tc.wantCode == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			hubErr, ok := err.(*hub_model.Error)
			require.True(t, ok)
			assert.Equal(t, tc.wantCode, hubErr.Code)
		})
	}
}

// TestWorkingDaysMaskEdgeCases walks the mask table AGENTS.md's mutation proof (d) targets: a
// wrong mask, or the mask ignored altogether, changes at least one of these.
func TestWorkingDaysMaskEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end time.Time
		mask       int
		want       []time.Time
	}{
		{
			name:  "Sunday bit alone over a full week",
			start: aMonday, end: aMonday.AddDate(0, 0, 6), mask: 1, // bit 0 = Sunday
			want: []time.Time{aMonday.AddDate(0, 0, 6)}, // the following Sunday
		},
		{
			name:  "every bit set keeps every day",
			start: aMonday, end: aMonday.AddDate(0, 0, 3), mask: 127,
			want: []time.Time{aMonday, aMonday.AddDate(0, 0, 1), aMonday.AddDate(0, 0, 2), aMonday.AddDate(0, 0, 3)},
		},
		{
			name:  "default 62 is Monday through Friday, weekend excluded",
			start: aMonday, end: aMonday.AddDate(0, 0, 6), mask: DefaultWorkdays,
			want: []time.Time{aMonday, aMonday.AddDate(0, 0, 1), aMonday.AddDate(0, 0, 2), aMonday.AddDate(0, 0, 3), aMonday.AddDate(0, 0, 4)},
		},
		{
			name:  "a mask matching no day in the window falls back to the start day",
			start: aMonday, end: aMonday.AddDate(0, 0, 6), mask: 0,
			want: []time.Time{aMonday},
		},
		{
			name:  "a single-day window, on a working day",
			start: aMonday, end: aMonday, mask: DefaultWorkdays,
			want: []time.Time{aMonday},
		},
		{
			name:  "end before start is still read start to end",
			start: aMonday.AddDate(0, 0, 2), end: aMonday, mask: DefaultWorkdays,
			want: []time.Time{aMonday, aMonday.AddDate(0, 0, 1), aMonday.AddDate(0, 0, 2)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := WorkingDays(tc.start, tc.end, tc.mask)
			require := assert.New(t)
			require.Len(got, len(tc.want))
			for i, day := range got {
				require.True(day.Equal(tc.want[i]), "day %d: got %v want %v", i, day, tc.want[i])
			}
		})
	}
}

func TestSpreadLoadEvenSplitOverWorkingDays(t *testing.T) {
	from, to := aMonday, aMonday.AddDate(0, 0, 4)
	caps := map[int64]Capacity{1: {HoursPerDay: 8, Utilization: 0.8, Workdays: DefaultWorkdays}}
	items := []LoadItem{
		{IssueID: 1, UserID: 1, RemainingSeconds: 5 * 3600, StartUnix: from.Unix(), EndUnix: to.Unix()},
	}
	lanes := SpreadLoad(items, caps, from, to)
	require := assert.New(t)
	require.Len(lanes, 1)
	require.Len(lanes[0].Days, 5)
	for _, day := range lanes[0].Days {
		require.InEpsilon(1.0, day.LoadHours, 0.0001, "5 hours over 5 working days is 1 hour a day")
	}
}

func TestSpreadLoadFlagsAnOverDay(t *testing.T) {
	from, to := aMonday, aMonday
	caps := map[int64]Capacity{1: {HoursPerDay: 6, Utilization: 0.5, Workdays: DefaultWorkdays}} // 3h available
	items := []LoadItem{
		{IssueID: 1, UserID: 1, RemainingSeconds: 16 * 3600, StartUnix: from.Unix(), EndUnix: to.Unix()},
	}
	lanes := SpreadLoad(items, caps, from, to)
	require := assert.New(t)
	require.Len(lanes, 1)
	require.Len(lanes[0].Days, 1)
	day := lanes[0].Days[0]
	require.InEpsilon(16.0, day.LoadHours, 0.0001)
	require.InEpsilon(3.0, day.AvailableHours, 0.0001)
	require.True(day.Over, "16h of load against 3h available is over")
	require.True(lanes[0].Over)
}

// TestSpreadLoadOverBoundaryIsStrictlyGreater is mutation proof (a): flipping the over
// comparison to >= must fail this, since equal load and availability is exactly the boundary
// case that is not over.
func TestSpreadLoadOverBoundaryIsStrictlyGreater(t *testing.T) {
	from, to := aMonday, aMonday
	caps := map[int64]Capacity{1: {HoursPerDay: 8, Utilization: 1, Workdays: DefaultWorkdays}} // 8h available
	items := []LoadItem{
		{IssueID: 1, UserID: 1, RemainingSeconds: 8 * 3600, StartUnix: from.Unix(), EndUnix: to.Unix()},
	}
	lanes := SpreadLoad(items, caps, from, to)
	assert.False(t, lanes[0].Days[0].Over, "load exactly equal to availability is not over")
}

func TestSpreadLoadListsZeroRemainingAsUnestimatedWithNoLoad(t *testing.T) {
	from, to := aMonday, aMonday
	caps := map[int64]Capacity{1: {HoursPerDay: 8, Utilization: 0.8, Workdays: DefaultWorkdays}}
	items := []LoadItem{
		{IssueID: 9, Number: 9, Title: "done already", UserID: 1, RemainingSeconds: 0, StartUnix: from.Unix(), EndUnix: to.Unix()},
	}
	lanes := SpreadLoad(items, caps, from, to)
	require := assert.New(t)
	require.Len(lanes, 1)
	require.Zero(lanes[0].Days[0].LoadHours)
	require.Equal([]UnestimatedIssue{{IssueID: 9, Number: 9, Title: "done already"}}, lanes[0].Unestimated)
}

// TestSpreadLoadAlwaysHasOneLanePerCapacityUser is what "a lane exists even with no load"
// means at the pure-function level: a user with a capacity row but no item still gets a lane.
func TestSpreadLoadAlwaysHasOneLanePerCapacityUser(t *testing.T) {
	from, to := aMonday, aMonday
	caps := map[int64]Capacity{1: {HoursPerDay: 8, Utilization: 0.8, Workdays: DefaultWorkdays}, 2: {HoursPerDay: 8, Utilization: 0.8, Workdays: DefaultWorkdays}}
	lanes := SpreadLoad(nil, caps, from, to)
	require := assert.New(t)
	require.Len(lanes, 2)
	require.Equal(int64(1), lanes[0].UserID)
	require.Equal(int64(2), lanes[1].UserID)
}

// TestSpreadLoadClipsToTheWindow is mutation-relevant for the clipping check: a day outside
// [from, to] must not appear at all, and must not be folded into a boundary day either.
func TestSpreadLoadClipsToTheWindow(t *testing.T) {
	from, to := aMonday, aMonday.AddDate(0, 0, 1)
	caps := map[int64]Capacity{1: {HoursPerDay: 8, Utilization: 0.8, Workdays: DefaultWorkdays}}
	items := []LoadItem{
		// Spans five working days, only two of which are inside [from, to].
		{IssueID: 1, UserID: 1, RemainingSeconds: 5 * 3600, StartUnix: aMonday.Unix(), EndUnix: aMonday.AddDate(0, 0, 4).Unix()},
	}
	lanes := SpreadLoad(items, caps, from, to)
	require := assert.New(t)
	require.Len(lanes[0].Days, 2)
	require.InEpsilon(2.0, lanes[0].TotalLoadHours, 0.0001, "only the two in-window days' 1h each")
}

func TestSprintLoadWholeRemainingAndAvailability(t *testing.T) {
	sprintStart, sprintEnd := aMonday, aMonday.AddDate(0, 0, 4) // Mon-Fri, 5 working days
	caps := map[int64]Capacity{1: {HoursPerDay: 8, Utilization: 0.8, Workdays: DefaultWorkdays}}
	sprints := []Sprint{{MilestoneID: 1, Title: "Sprint 1", StartUnix: sprintStart.Unix(), EndUnix: sprintEnd.Unix()}}
	items := []LoadItem{
		{IssueID: 1, UserID: 1, MilestoneID: 1, RemainingSeconds: 10 * 3600, Points: 3, StartUnix: sprintStart.Unix(), EndUnix: sprintEnd.Unix()},
		{IssueID: 2, UserID: 1, MilestoneID: 1, RemainingSeconds: 5 * 3600, Points: 2, StartUnix: sprintStart.AddDate(0, 0, 1).Unix(), EndUnix: sprintEnd.Unix()},
		// A different milestone entirely; its bar window even sits inside the sprint's own.
		{IssueID: 3, UserID: 1, MilestoneID: 2, RemainingSeconds: 100 * 3600, Points: 99, StartUnix: sprintStart.Unix(), EndUnix: sprintEnd.Unix()},
	}
	rows := SprintLoad(items, caps, sprints)
	require := assert.New(t)
	require.Len(rows, 1)
	row := rows[0]
	require.Equal(5, row.WorkingDays)
	require.Len(row.Lanes, 1)
	lane := row.Lanes[0]
	require.InEpsilon(15.0, lane.LoadHours, 0.0001, "10h + 5h, the third item belongs to a different milestone")
	require.InEpsilon(5*8*0.8, lane.AvailableHours, 0.0001)
	require.False(lane.Over)
	require.InEpsilon(5.0, lane.Points, 0.0001)
}

// TestSprintLoadFlagsAnOverSprint is mutation proof (a)'s sprint-side counterpart.
func TestSprintLoadFlagsAnOverSprint(t *testing.T) {
	sprintStart, sprintEnd := aMonday, aMonday
	caps := map[int64]Capacity{1: {HoursPerDay: 6, Utilization: 0.5, Workdays: DefaultWorkdays}} // 3h available
	sprints := []Sprint{{MilestoneID: 1, Title: "Sprint 1", StartUnix: sprintStart.Unix(), EndUnix: sprintEnd.Unix()}}
	items := []LoadItem{{IssueID: 1, UserID: 1, MilestoneID: 1, RemainingSeconds: 16 * 3600, StartUnix: sprintStart.Unix(), EndUnix: sprintEnd.Unix()}}
	rows := SprintLoad(items, caps, sprints)
	assert.True(t, rows[0].Lanes[0].Over)
}

// TestSprintLoadMatchesByMilestoneNotDate is mutation proof for item 2: an item's own bar
// window can sit entirely inside a sprint's window and still not belong to it, because its
// MilestoneID names a different milestone; conversely an item's bar can sit outside the sprint
// window and still belong, because its MilestoneID matches. An item with no milestone at all
// (MilestoneID zero) loads no sprint even when a sprint happens to be unscheduled (MilestoneID
// zero can never occur for a real milestone, but the check must not rely on that).
func TestSprintLoadMatchesByMilestoneNotDate(t *testing.T) {
	sprintStart, sprintEnd := aMonday, aMonday.AddDate(0, 0, 4)
	caps := map[int64]Capacity{1: {HoursPerDay: 8, Utilization: 0.8, Workdays: DefaultWorkdays}}
	sprints := []Sprint{{MilestoneID: 1, Title: "Sprint A", StartUnix: sprintStart.Unix(), EndUnix: sprintEnd.Unix()}}
	items := []LoadItem{
		// In milestone 1, but its own bar sits weeks outside the sprint's window.
		{IssueID: 1, UserID: 1, MilestoneID: 1, RemainingSeconds: 10 * 3600, StartUnix: sprintStart.AddDate(0, 0, 30).Unix(), EndUnix: sprintEnd.AddDate(0, 0, 30).Unix()},
		// No milestone at all, even though its bar sits squarely inside the sprint's window.
		{IssueID: 2, UserID: 1, RemainingSeconds: 20 * 3600, StartUnix: sprintStart.Unix(), EndUnix: sprintEnd.Unix()},
	}
	rows := SprintLoad(items, caps, sprints)
	require := assert.New(t)
	require.Len(rows, 1)
	require.Len(rows[0].Lanes, 1, "only the milestone-matched item seeds a lane")
	require.InEpsilon(10.0, rows[0].Lanes[0].LoadHours, 0.0001, "the unmatched, no-milestone item contributes nothing")
}
