// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rulerStart is a Monday, so the week ticks land on it and the labels are checkable by eye.
var rulerStart = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)

// TestDeliveryRulerUnitFollowsTheSpanAtEveryBoundary walks each row of the unit table and the
// second either side of it: a ruler that stays in days across a two-year plan is the failure
// this catches, and so is one that jumps a unit early.
func TestDeliveryRulerUnitFollowsTheSpanAtEveryBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  time.Time
		unit string
	}{
		{"zero span", rulerStart, RulerDay},
		{"a fortnight exactly", rulerStart.AddDate(0, 0, 14), RulerDay},
		{"one second past a fortnight", rulerStart.AddDate(0, 0, 14).Add(time.Second), RulerWeek},
		{"ten weeks exactly", rulerStart.AddDate(0, 0, 70), RulerWeek},
		{"one second past ten weeks", rulerStart.AddDate(0, 0, 70).Add(time.Second), RulerMonth},
		{"eighteen months exactly", rulerStart.AddDate(0, 18, 0), RulerMonth},
		{"one second past eighteen months", rulerStart.AddDate(0, 18, 0).Add(time.Second), RulerQuarter},
		{"two years", rulerStart.AddDate(2, 0, 0), RulerQuarter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unit, ticks := RulerFor(rulerStart.Unix(), tc.end.Unix())
			assert.Equal(t, tc.unit, unit)
			require.NotEmpty(t, ticks, "a span always has at least the tick it starts inside")
		})
	}
}

// TestDeliveryRulerTicksAlignToUnitBoundariesInUTC is what makes the ruler readable: a tick
// is a date, not an offset from whenever the first bar happens to start.
func TestDeliveryRulerTicksAlignToUnitBoundariesInUTC(t *testing.T) {
	// Mid-afternoon on a Wednesday: every unit has to round back to its own boundary.
	from := time.Date(2026, 3, 4, 15, 30, 0, 0, time.UTC)

	unit, ticks := RulerFor(from.Unix(), from.AddDate(0, 0, 3).Unix())
	assert.Equal(t, RulerDay, unit)
	assert.Equal(t, "Wed 4", ticks[0].Label)
	assert.Equal(t, time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC).Unix(), ticks[0].Unix)
	assert.Equal(t, "Sat 7", ticks[len(ticks)-1].Label)

	unit, ticks = RulerFor(from.Unix(), from.AddDate(0, 0, 30).Unix())
	assert.Equal(t, RulerWeek, unit)
	assert.Equal(t, "w/c 2 Mar", ticks[0].Label, "the week commences on the Monday before the span")
	assert.Equal(t, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC).Unix(), ticks[0].Unix)
	assert.Equal(t, "w/c 9 Mar", ticks[1].Label)

	unit, ticks = RulerFor(from.Unix(), from.AddDate(1, 0, 0).Unix())
	assert.Equal(t, RulerMonth, unit)
	assert.Equal(t, "Mar 2026", ticks[0].Label)
	assert.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).Unix(), ticks[0].Unix)

	unit, ticks = RulerFor(from.Unix(), from.AddDate(3, 0, 0).Unix())
	assert.Equal(t, RulerQuarter, unit)
	assert.Equal(t, "Q1 2026", ticks[0].Label)
	assert.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), ticks[0].Unix)
	assert.Equal(t, "Q2 2026", ticks[1].Label)
}

// TestDeliveryRulerCoversTheWholeSpanAndIsBounded checks the two ends of the tick loop: the
// last tick is inside the span, and an absurd span does not build gridlines nobody reads.
func TestDeliveryRulerCoversTheWholeSpanAndIsBounded(t *testing.T) {
	_, ticks := RulerFor(rulerStart.Unix(), rulerStart.AddDate(0, 0, 14).Unix())
	assert.Len(t, ticks, 15, "both endpoints are inside a labelled day")
	assert.Equal(t, rulerStart.AddDate(0, 0, 14).Unix(), ticks[len(ticks)-1].Unix)

	_, ticks = RulerFor(rulerStart.Unix(), rulerStart.AddDate(5000, 0, 0).Unix())
	assert.Len(t, ticks, maxTicks)

	// A backwards span is a caller error, not a reason to draw nothing.
	unit, ticks := RulerFor(rulerStart.AddDate(0, 0, 3).Unix(), rulerStart.Unix())
	assert.Equal(t, RulerDay, unit)
	assert.Len(t, ticks, 4)
}
