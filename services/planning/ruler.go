// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"strconv"
	"time"
)

// The chart's time axis, decided here so every client draws the same ruler. The unit follows
// the span; the write stays a day, so a drag at quarter zoom still lands on a date.

// Tick is one labelled gridline, at a unit boundary in UTC.
type Tick struct {
	Unix  int64  `json:"unix"`
	Label string `json:"label"`
}

// The ruler units, coarsest last.
const (
	RulerDay     = "day"
	RulerWeek    = "week"
	RulerMonth   = "month"
	RulerQuarter = "quarter"
)

const maxTicks = 400 // a chart nobody reads thousands of gridlines off

// RulerFor picks the unit for a span and lays out its ticks: day to a fortnight, week to ten
// weeks, month to eighteen months, quarter beyond. The first tick is the boundary before start.
func RulerFor(startUnix, endUnix int64) (unit string, ticks []Tick) {
	if endUnix < startUnix {
		startUnix, endUnix = endUnix, startUnix
	}
	start := time.Unix(startUnix, 0).UTC()
	end := time.Unix(endUnix, 0).UTC()

	switch {
	case !end.After(start.AddDate(0, 0, 14)):
		return RulerDay, ticksFrom(startOfDay(start), end, func(at time.Time) time.Time { return at.AddDate(0, 0, 1) },
			func(at time.Time) string { return at.Format("Mon 2") })
	case !end.After(start.AddDate(0, 0, 70)):
		return RulerWeek, ticksFrom(startOfWeek(start), end, func(at time.Time) time.Time { return at.AddDate(0, 0, 7) },
			func(at time.Time) string { return "w/c " + at.Format("2 Jan") })
	case !end.After(start.AddDate(0, 18, 0)):
		return RulerMonth, ticksFrom(startOfMonth(start), end, func(at time.Time) time.Time { return at.AddDate(0, 1, 0) },
			func(at time.Time) string { return at.Format("Jan 2006") })
	}
	return RulerQuarter, ticksFrom(startOfQuarter(start), end, func(at time.Time) time.Time { return at.AddDate(0, 3, 0) },
		quarterLabel)
}

func ticksFrom(at, end time.Time, next func(time.Time) time.Time, label func(time.Time) string) []Tick {
	ticks := make([]Tick, 0, 16)
	for !at.After(end) && len(ticks) < maxTicks {
		ticks = append(ticks, Tick{Unix: at.Unix(), Label: label(at)})
		at = next(at)
	}
	return ticks
}

func startOfDay(at time.Time) time.Time {
	return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
}

// startOfWeek is the Monday on or before the day, which is what "w/c" names.
func startOfWeek(at time.Time) time.Time {
	day := startOfDay(at)
	return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
}

func startOfMonth(at time.Time) time.Time {
	return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func startOfQuarter(at time.Time) time.Time {
	return time.Date(at.Year(), at.Month()-(at.Month()-1)%3, 1, 0, 0, 0, 0, time.UTC)
}

func quarterLabel(at time.Time) string {
	return "Q" + strconv.Itoa(int(at.Month()-1)/3+1) + " " + strconv.Itoa(at.Year())
}
