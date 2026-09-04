// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"strings"
	"testing"

	hub_model "gitea.dev/models/hub"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeEnvironmentName(t *testing.T) {
	assert.Equal(t, "prod", NormalizeEnvironmentName("  PROD "))
	assert.Empty(t, NormalizeEnvironmentName("   "))
}

func TestValidateEnvironment(t *testing.T) {
	valid := &Environment{Name: "prod", ReviewPolicy: PolicyNone, RequiredReviewers: 1}
	require.NoError(t, ValidateEnvironment(valid))

	cases := []struct {
		name      string
		env       *Environment
		wantInMsg string
	}{
		{"empty name", &Environment{Name: " ", ReviewPolicy: PolicyNone, RequiredReviewers: 1}, "empty"},
		{"over-long name", &Environment{Name: strings.Repeat("x", 65), ReviewPolicy: PolicyNone, RequiredReviewers: 1}, "65 characters"},
		{"unknown policy", &Environment{Name: "prod", ReviewPolicy: "everyone", RequiredReviewers: 1}, "everyone"},
		{"unsatisfiable gate", &Environment{Name: "prod", ReviewPolicy: PolicyOthersOnly, RequiredReviewers: 0}, "unsatisfiable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateEnvironment(c.env)
			require.Error(t, err)
			var hubErr *hub_model.Error
			require.ErrorAs(t, err, &hubErr)
			assert.Contains(t, hubErr.Message, c.wantInMsg)
			assert.NotEmpty(t, hubErr.SuggestedAction, "every error carries a suggested next action")
		})
	}
}

func TestDefaultPolicyIsNone(t *testing.T) {
	assert.Equal(t, PolicyNone, ReviewPolicies[0])
	assert.False(t, new(Environment).ReleasesOnly, "a new environment refuses no release kind")
}

// TestValidateEnvironmentWaitMinutesBoundary pins the numeric limit at both edges: 10080
// (a week) is accepted, 10081 is refused as bad_wait.
func TestValidateEnvironmentWaitMinutesBoundary(t *testing.T) {
	ok := &Environment{Name: "prod", ReviewPolicy: PolicyNone, RequiredReviewers: 1, WaitMinutes: 10080}
	assert.NoError(t, ValidateEnvironment(ok))

	bad := &Environment{Name: "prod", ReviewPolicy: PolicyNone, RequiredReviewers: 1, WaitMinutes: 10081}
	err := ValidateEnvironment(bad)
	require.Error(t, err)
	var hubErr *hub_model.Error
	require.ErrorAs(t, err, &hubErr)
	assert.Equal(t, "bad_wait", hubErr.Code)
	assert.Equal(t, 422, hubErr.Status)

	assert.Error(t, ValidateEnvironment(&Environment{Name: "prod", ReviewPolicy: PolicyNone, RequiredReviewers: 1, WaitMinutes: -1}))
}

// TestValidateEnvironmentDeployWindow tables the bad_window cases, including the mask and
// minute boundaries, and confirms a nil window and an explicit always-open zero window both
// validate.
func TestValidateEnvironmentDeployWindow(t *testing.T) {
	base := func(w *DeployWindow) *Environment {
		return &Environment{Name: "prod", ReviewPolicy: PolicyNone, RequiredReviewers: 1, DeployWindow: w}
	}
	assert.NoError(t, ValidateEnvironment(base(nil)), "no window at all is always open")
	assert.NoError(t, ValidateEnvironment(base(&DeployWindow{})), "a zero window is always open")
	assert.NoError(t, ValidateEnvironment(base(&DeployWindow{DaysMask: 0b111_1111, FromMinute: 0, ToMinute: minutesPerDay, Timezone: "UTC"})))
	assert.NoError(t, ValidateEnvironment(base(&DeployWindow{DaysMask: 1, FromMinute: 22 * 60, ToMinute: 6 * 60, Timezone: "UTC"})),
		"from_minute after to_minute is an overnight window, not an error")

	cases := []struct {
		name string
		w    *DeployWindow
	}{
		{"mask below range", &DeployWindow{DaysMask: -1, ToMinute: 60, Timezone: "UTC"}},
		{"mask above range", &DeployWindow{DaysMask: 128, ToMinute: 60, Timezone: "UTC"}},
		{"from below range", &DeployWindow{DaysMask: 1, FromMinute: -1, ToMinute: 60, Timezone: "UTC"}},
		{"to above range", &DeployWindow{DaysMask: 1, FromMinute: 0, ToMinute: minutesPerDay + 1, Timezone: "UTC"}},
		{"from equal to never opens", &DeployWindow{DaysMask: 1, FromMinute: 60, ToMinute: 60, Timezone: "UTC"}},
		{"unknown timezone", &DeployWindow{DaysMask: 1, FromMinute: 0, ToMinute: 60, Timezone: "Not/AZone"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateEnvironment(base(c.w))
			require.Error(t, err)
			var hubErr *hub_model.Error
			require.ErrorAs(t, err, &hubErr)
			assert.Equal(t, "bad_window", hubErr.Code)
			assert.Equal(t, 422, hubErr.Status)
			assert.NotEmpty(t, hubErr.SuggestedAction)
		})
	}
}

// TestValidateEnvironmentRequiredStatusContexts tables the bad_contexts cases including the
// count boundary at 20/21 entries.
func TestValidateEnvironmentRequiredStatusContexts(t *testing.T) {
	base := func(contexts []string) *Environment {
		return &Environment{Name: "prod", ReviewPolicy: PolicyNone, RequiredReviewers: 1, RequiredStatusContexts: contexts}
	}
	assert.NoError(t, ValidateEnvironment(base(nil)))

	twenty := make([]string, 20)
	for i := range twenty {
		twenty[i] = "ci/check"
	}
	assert.NoError(t, ValidateEnvironment(base(twenty)))

	twentyOne := append(append([]string(nil), twenty...), "ci/one-more")
	require.Len(t, twentyOne, 21)
	err := ValidateEnvironment(base(twentyOne))
	require.Error(t, err)
	var hubErr *hub_model.Error
	require.ErrorAs(t, err, &hubErr)
	assert.Equal(t, "bad_contexts", hubErr.Code)
	assert.Equal(t, 422, hubErr.Status)

	err = ValidateEnvironment(base([]string{" "}))
	require.Error(t, err)
	require.ErrorAs(t, err, &hubErr)
	assert.Equal(t, "bad_contexts", hubErr.Code)

	assert.NoError(t, ValidateEnvironment(base([]string{strings.Repeat("x", 255)})), "255 characters is the accepted boundary")

	err = ValidateEnvironment(base([]string{strings.Repeat("x", 256)}))
	require.Error(t, err)
	require.ErrorAs(t, err, &hubErr)
	assert.Equal(t, "bad_contexts", hubErr.Code)
}

// TestDependencyGraph tables the pure cycle check: a missing dependency, a self-cycle, a
// longer cycle, and an acyclic diamond that must pass.
func TestDependencyGraph(t *testing.T) {
	cases := []struct {
		name    string
		envs    []*Environment
		wantErr bool
	}{
		{
			"acyclic diamond",
			[]*Environment{
				{Name: "dev"},
				{Name: "qa", DependsOn: []string{"dev"}},
				{Name: "uat", DependsOn: []string{"dev"}},
				{Name: "prod", DependsOn: []string{"qa", "uat"}},
			},
			false,
		},
		{
			"missing dependency",
			[]*Environment{{Name: "prod", DependsOn: []string{"staging"}}},
			true,
		},
		{
			"self cycle",
			[]*Environment{{Name: "prod", DependsOn: []string{"prod"}}},
			true,
		},
		{
			"longer cycle",
			[]*Environment{
				{Name: "a", DependsOn: []string{"b"}},
				{Name: "b", DependsOn: []string{"c"}},
				{Name: "c", DependsOn: []string{"a"}},
			},
			true,
		},
		{
			"no dependencies at all",
			[]*Environment{{Name: "prod"}},
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := DependencyGraph(c.envs)
			if !c.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var hubErr *hub_model.Error
			require.ErrorAs(t, err, &hubErr)
			assert.Equal(t, "cycle", hubErr.Code)
			assert.Equal(t, 422, hubErr.Status)
		})
	}
}
