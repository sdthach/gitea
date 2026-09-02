// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeEnvironmentName(t *testing.T) {
	assert.Equal(t, "prod", NormalizeEnvironmentName("  PROD "))
	assert.Empty(t, NormalizeEnvironmentName("   "))
}

func TestValidateEnvironment(t *testing.T) {
	valid := &Environment{Name: "prod", ApprovalPolicy: PolicyNone, RequiredApprovals: 1}
	require.NoError(t, ValidateEnvironment(valid))

	cases := []struct {
		name      string
		env       *Environment
		wantInMsg string
	}{
		{"empty name", &Environment{Name: " ", ApprovalPolicy: PolicyNone, RequiredApprovals: 1}, "empty"},
		{"over-long name", &Environment{Name: strings.Repeat("x", 65), ApprovalPolicy: PolicyNone, RequiredApprovals: 1}, "65 characters"},
		{"unknown policy", &Environment{Name: "prod", ApprovalPolicy: "everyone", RequiredApprovals: 1}, "everyone"},
		{"unsatisfiable gate", &Environment{Name: "prod", ApprovalPolicy: PolicyOthersOnly, RequiredApprovals: 0}, "unsatisfiable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateEnvironment(c.env)
			require.Error(t, err)
			var hubErr *Error
			require.ErrorAs(t, err, &hubErr)
			assert.Contains(t, hubErr.Message, c.wantInMsg)
			assert.NotEmpty(t, hubErr.SuggestedAction, "every error carries a suggested next action")
		})
	}
}

func TestDefaultPolicyIsNone(t *testing.T) {
	assert.Equal(t, PolicyNone, ApprovalPolicies[0])
	assert.False(t, new(Environment).RequireFullRelease, "a new environment refuses no release kind")
}
