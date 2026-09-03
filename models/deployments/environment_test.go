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
