// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestApplyEnvironmentScope is SC 17: PROD_DB_PASS scoped to prod is present under
// environment: prod, absent from a job declaring environment: qa, and absent from a job
// declaring no environment at all.
func TestApplyEnvironmentScope(t *testing.T) {
	secrets := map[string]string{
		"GITHUB_TOKEN":   "auto",
		"GITEA_TOKEN":    "auto",
		"PROD_DB_PASS":   "prod-value",
		"QA_DB_PASS":     "qa-value",
		"SHARED_API_KEY": "shared-value",
	}
	scopes := map[string]string{
		"PROD_DB_PASS": "prod",
		"QA_DB_PASS":   "qa",
	}

	cases := []struct {
		name    string
		jobEnv  string
		present []string
		absent  []string
	}{
		{
			name: "job declares prod", jobEnv: "prod",
			present: []string{"PROD_DB_PASS", "SHARED_API_KEY", "GITHUB_TOKEN", "GITEA_TOKEN"},
			absent:  []string{"QA_DB_PASS"},
		},
		{
			name: "job declares a different environment", jobEnv: "qa",
			present: []string{"QA_DB_PASS", "SHARED_API_KEY"},
			absent:  []string{"PROD_DB_PASS"},
		},
		{
			name: "job declares no environment", jobEnv: "",
			present: []string{"SHARED_API_KEY", "GITHUB_TOKEN", "GITEA_TOKEN"},
			absent:  []string{"PROD_DB_PASS", "QA_DB_PASS"},
		},
		{
			name: "job declares an environment nothing is scoped to", jobEnv: "staging",
			present: []string{"SHARED_API_KEY"},
			absent:  []string{"PROD_DB_PASS", "QA_DB_PASS"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := applyEnvironmentScope(secrets, scopes, c.jobEnv)
			for _, name := range c.present {
				assert.Contains(t, got, name, "%s must resolve for a job declaring %q", name, c.jobEnv)
				assert.Equal(t, secrets[name], got[name])
			}
			for _, name := range c.absent {
				assert.NotContains(t, got, name, "%s must be unreachable from a job declaring %q", name, c.jobEnv)
			}
		})
	}
}

func TestApplyEnvironmentScopeIsCaseInsensitive(t *testing.T) {
	got := applyEnvironmentScope(
		map[string]string{"PROD_DB_PASS": "v"},
		map[string]string{"prod_db_pass": "PROD"},
		"Prod",
	)
	assert.Contains(t, got, "PROD_DB_PASS", "names and environments are identifiers, compared case-insensitively")
}

func TestApplyEnvironmentScopeLeavesUnscopedSecretsAlone(t *testing.T) {
	secrets := map[string]string{"A": "1", "B": "2"}
	assert.Equal(t, secrets, applyEnvironmentScope(secrets, nil, ""),
		"with nothing scoped, adding the fork changes no existing behaviour")
}

func TestAutoTokensSurviveEveryScope(t *testing.T) {
	got := applyEnvironmentScope(
		map[string]string{"GITHUB_TOKEN": "a", "GITEA_TOKEN": "b"},
		map[string]string{"GITHUB_TOKEN": "prod", "GITEA_TOKEN": "prod"},
		"qa",
	)
	assert.Equal(t, map[string]string{"GITHUB_TOKEN": "a", "GITEA_TOKEN": "b"}, got,
		"the per-task tokens are generated, not configured, so they are never scoped away")
}
