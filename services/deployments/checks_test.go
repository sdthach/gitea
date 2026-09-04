// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"testing"
	"time"

	deployments_model "gitea.dev/models/deployments"
	git_model "gitea.dev/models/git"
	"gitea.dev/models/unittest"
	"gitea.dev/modules/commitstatus"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func unixIn(loc *time.Location, year int, month time.Month, day, hour, minute int) int64 {
	return time.Date(year, month, day, hour, minute, 0, 0, loc).Unix()
}

// weekdayMask ORs the bits for the given time.Weekday values, bit 0 Sunday .. bit 6 Saturday.
func weekdayMask(days ...time.Weekday) int {
	mask := 0
	for _, d := range days {
		mask |= 1 << uint(d)
	}
	return mask
}

func TestWindowOpen(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	weekdays := &deployments_model.DeployWindow{
		DaysMask:   weekdayMask(time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday),
		FromMinute: 9 * 60, ToMinute: 17 * 60, Timezone: "America/New_York",
	}

	cases := []struct {
		name string
		w    *deployments_model.DeployWindow
		now  int64
		want bool
	}{
		{"nil window is always open", nil, unixIn(loc, 2026, time.January, 3, 3, 0), true},
		{"zero window is always open", &deployments_model.DeployWindow{}, unixIn(loc, 2026, time.January, 3, 3, 0), true},
		{"weekday inside hours", weekdays, unixIn(loc, 2026, time.March, 3, 10, 0), true},  // Tuesday 10am
		{"weekday before hours", weekdays, unixIn(loc, 2026, time.March, 3, 8, 59), false}, // Tuesday 08:59
		{"weekday at close is shut", weekdays, unixIn(loc, 2026, time.March, 3, 17, 0), false},
		{"weekend is shut", weekdays, unixIn(loc, 2026, time.March, 7, 10, 0), false}, // Saturday 10am
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			open, err := windowOpen(c.now, c.w)
			require.NoError(t, err)
			assert.Equal(t, c.want, open)
		})
	}

	_, err = windowOpen(0, &deployments_model.DeployWindow{DaysMask: 1, ToMinute: 60, Timezone: "Not/AZone"})
	assert.Error(t, err, "an invalid timezone is reported rather than silently treated as open or closed")
}

// TestWindowOpenAcrossFallBack covers the one hour America/New_York repeats every autumn:
// 01:00-01:59 happens twice, an hour apart in UTC. A window covering that hour must read open
// both times, since windowOpen answers on local wall time, not on the UTC instant.
func TestWindowOpenAcrossFallBack(t *testing.T) {
	window := &deployments_model.DeployWindow{
		DaysMask: weekdayMask(time.Sunday), FromMinute: 60, ToMinute: 120, Timezone: "America/New_York",
	}
	firstPass, err := time.Parse(time.RFC3339, "2026-11-01T05:30:00Z") // 01:30 EDT
	require.NoError(t, err)
	secondPass, err := time.Parse(time.RFC3339, "2026-11-01T06:30:00Z") // 01:30 EST, repeated
	require.NoError(t, err)

	open, err := windowOpen(firstPass.Unix(), window)
	require.NoError(t, err)
	assert.True(t, open, "01:30 EDT, the first time it occurs, is inside the window")

	open, err = windowOpen(secondPass.Unix(), window)
	require.NoError(t, err)
	assert.True(t, open, "01:30 EST, the same wall-clock hour repeated, is inside the window too")
}

// TestWindowOpenAcrossSpringForward covers the hour America/New_York skips every spring:
// 02:00-02:59 never happens on that Sunday. A window entirely inside the skipped hour must
// still answer without error either side of the jump.
func TestWindowOpenAcrossSpringForward(t *testing.T) {
	window := &deployments_model.DeployWindow{
		DaysMask: weekdayMask(time.Sunday), FromMinute: 120, ToMinute: 150, Timezone: "America/New_York",
	}
	beforeJump, err := time.Parse(time.RFC3339, "2026-03-08T06:30:00Z") // 01:30 EST
	require.NoError(t, err)
	afterJump, err := time.Parse(time.RFC3339, "2026-03-08T07:30:00Z") // 03:30 EDT
	require.NoError(t, err)

	open, err := windowOpen(beforeJump.Unix(), window)
	require.NoError(t, err)
	assert.False(t, open, "01:30 is before the window's 02:00 start")

	open, err = windowOpen(afterJump.Unix(), window)
	require.NoError(t, err)
	assert.False(t, open, "03:30 is after the window's 02:30 close; the 02:00-02:29 slice never occurred that day")
}

func TestNextOpening(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	weekdays := &deployments_model.DeployWindow{
		DaysMask:   weekdayMask(time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday),
		FromMinute: 9 * 60, ToMinute: 17 * 60, Timezone: "America/New_York",
	}

	// Tuesday 6pm: closed for the day, opens Wednesday 9am.
	now := unixIn(loc, 2026, time.March, 3, 18, 0)
	next, err := NextOpening(now, weekdays)
	require.NoError(t, err)
	assert.Equal(t, unixIn(loc, 2026, time.March, 4, 9, 0), next)

	// Friday 6pm: closed for the weekend, opens Monday 9am.
	now = unixIn(loc, 2026, time.March, 6, 18, 0)
	next, err = NextOpening(now, weekdays)
	require.NoError(t, err)
	assert.Equal(t, unixIn(loc, 2026, time.March, 9, 9, 0), next)
}

// TestNextOpeningAcrossSpringForward: a window opening at 02:30 local time on the one Sunday
// that hour is skipped still returns a time strictly after now — Go's own wall-clock
// normalization resolves the skipped instant rather than NextOpening looping forever.
func TestNextOpeningAcrossSpringForward(t *testing.T) {
	window := &deployments_model.DeployWindow{
		DaysMask: weekdayMask(time.Sunday), FromMinute: 150, ToMinute: 180, Timezone: "America/New_York",
	}
	now, err := time.Parse(time.RFC3339, "2026-03-08T00:00:00Z") // Saturday evening EST
	require.NoError(t, err)

	next, err := NextOpening(now.Unix(), window)
	require.NoError(t, err)
	assert.Greater(t, next, now.Unix())
}

func TestRequiredContextsCheck(t *testing.T) {
	statuses := []*git_model.CommitStatus{
		{Context: "ci/build", State: commitstatus.CommitStatusSuccess},
		{Context: "ci/lint", State: commitstatus.CommitStatusFailure},
	}

	assert.Equal(t, CheckPass, requiredContextsCheck(nil, statuses).State, "no required contexts always passes")

	c := requiredContextsCheck([]string{"ci/build"}, statuses)
	assert.Equal(t, CheckPass, c.State)

	c = requiredContextsCheck([]string{"ci/build", "ci/lint"}, statuses)
	assert.Equal(t, CheckFail, c.State, "a failing context fails the check, it never waits for one that already reported")
	assert.Contains(t, c.Reason, "ci/lint")

	c = requiredContextsCheck([]string{"ci/missing"}, statuses)
	assert.Equal(t, CheckFail, c.State, "a context that has not reported at all fails, the same as one that failed")
	assert.Contains(t, c.Reason, "ci/missing")
}

func TestAggregateCheckState(t *testing.T) {
	assert.Equal(t, CheckPass, AggregateCheckState(nil))
	assert.Equal(t, CheckPass, AggregateCheckState([]Check{{State: CheckPass}, {State: CheckPass}}))
	assert.Equal(t, CheckWait, AggregateCheckState([]Check{{State: CheckPass}, {State: CheckWait}}))
	assert.Equal(t, CheckFail, AggregateCheckState([]Check{{State: CheckWait}, {State: CheckFail}, {State: CheckPass}}),
		"a fail anywhere wins over a wait")
}

func TestWaitTimerCheck(t *testing.T) {
	env := &deployments_model.Environment{Name: "prod", WaitMinutes: 10}
	c := waitTimerCheck(env, 1000, 1000+9*60)
	assert.Equal(t, CheckWait, c.State)
	assert.Equal(t, int64(1000+600), c.RetryAt)

	c = waitTimerCheck(env, 1000, 1000+600)
	assert.Equal(t, CheckPass, c.State, "now equal to retry_at is open")

	c = waitTimerCheck(&deployments_model.Environment{Name: "prod"}, 1000, 1000)
	assert.Equal(t, CheckPass, c.State, "zero wait_minutes never holds")
}

func TestReleasesOnlyCheck(t *testing.T) {
	env := &deployments_model.Environment{Name: "prod", ReleasesOnly: true}
	c := releasesOnlyCheck(env, "v1.0", true)
	assert.Equal(t, CheckFail, c.State)
	assert.Contains(t, c.Reason, "v1.0")

	assert.Equal(t, CheckPass, releasesOnlyCheck(env, "v1.0", false).State)
	assert.Equal(t, CheckPass, releasesOnlyCheck(&deployments_model.Environment{Name: "prod"}, "v1.0", true).State)
}

func TestPriorDeploymentCheck(t *testing.T) {
	events := []Event{
		{ReleaseTag: "v1.0", Environment: "staging", Event: deployments_model.AuditSucceeded, OccurredUnix: 1, ID: 1},
	}
	required := &deployments_model.Environment{Name: "prod", DependsOn: []string{"staging"}, RequirePriorDeployment: true}

	assert.Equal(t, CheckPass, priorDeploymentCheck(required, "v1.0", events, false).State, "staging is live with v1.0")

	c := priorDeploymentCheck(required, "v2.0", events, false)
	assert.Equal(t, CheckWait, c.State, "staging has never held v2.0")

	assert.Equal(t, CheckPass, priorDeploymentCheck(required, "v2.0", events, true).State,
		"a granted override defers to the sequence decision that already ran")

	notRequired := &deployments_model.Environment{Name: "prod", DependsOn: []string{"staging"}}
	assert.Equal(t, CheckPass, priorDeploymentCheck(notRequired, "v2.0", events, false).State,
		"require_prior_deployment off leaves this to DecidePromotion's own warning")
}

// TestExclusiveLockCheck: a placeholder deployment sitting in the waiting state holds the
// lock exactly as a running one would.
func TestExclusiveLockCheck(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	env := &deployments_model.Environment{Name: "excl-lock-prod", ExclusiveLock: true}
	c, err := exclusiveLockCheck(ctx, 1, env)
	require.NoError(t, err)
	assert.Equal(t, CheckPass, c.State, "nothing is running yet")

	placeholder := &deployments_model.Deployment{RepoID: 1, Environment: "excl-lock-prod", ReleaseTag: "v1.0"}
	require.NoError(t, deployments_model.AppendPlaceholderDeployment(ctx, placeholder))

	c, err = exclusiveLockCheck(ctx, 1, env)
	require.NoError(t, err)
	assert.Equal(t, CheckWait, c.State, "a waiting placeholder holds the lock the same as a running deployment would")

	c, err = exclusiveLockCheck(ctx, 1, &deployments_model.Environment{Name: "excl-lock-prod"})
	require.NoError(t, err)
	assert.Equal(t, CheckPass, c.State, "exclusive_lock off never holds, whatever else is running")
}
