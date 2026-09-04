// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import "time"

// CurrentISOWeek is the timesheet's default window when neither from nor to is given: the
// Monday through Sunday containing now, in UTC calendar days.
func CurrentISOWeek(now time.Time) (time.Time, time.Time) {
	now = now.UTC().Truncate(24 * time.Hour)
	// time.Weekday is Sunday=0..Saturday=6; ISO weeks start Monday, so Sunday is 6 days after
	// its own Monday rather than 0.
	daysSinceMonday := (int(now.Weekday()) + 6) % 7
	monday := now.AddDate(0, 0, -daysSinceMonday)
	sunday := monday.AddDate(0, 0, 6)
	return monday, sunday
}

// Truncate keeps at most limit items, reporting whether items was longer — the same prefix-
// and-flag contract every capped list in this area answers with, kept generic so a caller's
// own row type never needs its own copy.
func Truncate[T any](items []T, limit int) ([]T, bool) {
	if len(items) > limit {
		return items[:limit], true
	}
	return items, false
}
