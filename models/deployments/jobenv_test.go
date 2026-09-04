// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"testing"

	"gitea.dev/modules/actions/jobparser"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const deployWorkflow = `name: deploy
on: workflow_dispatch
jobs:
  deploy-prod:
    runs-on: ubuntu-latest
    environment: prod
    steps:
      - run: ./deploy.sh
  deploy-qa:
    runs-on: ubuntu-latest
    environment:
      name: QA
      url: https://qa.example.invalid
    steps:
      - run: ./deploy.sh
  build:
    runs-on: ubuntu-latest
    steps:
      - run: make
  sneaky:
    runs-on: ubuntu-latest
    environment: ${{ github.event.inputs.env }}
    steps:
      - run: ./deploy.sh
`

func TestParseJobEnvironment(t *testing.T) {
	cases := []struct {
		jobID string
		want  string
	}{
		{"deploy-prod", "prod"},
		{"deploy-qa", "qa"},
		{"build", ""},
		{"no-such-job", ""},
		// An unevaluated expression resolves to no environment, so a workflow cannot reach
		// an environment-scoped secret through a value the fork has not resolved.
		{"sneaky", ""},
	}
	for _, c := range cases {
		t.Run(c.jobID, func(t *testing.T) {
			got, err := ParseJobEnvironment([]byte(deployWorkflow), c.jobID)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestParseJobEnvironmentRejectsUnparseableYAML(t *testing.T) {
	_, err := ParseJobEnvironment([]byte("jobs:\n  a: [unclosed\n"), "a")
	require.Error(t, err)
}

// TestEnvironmentIsAbsentFromWorkflowPayload records the measurement this design rests on:
// at the pin, jobparser.Job declares no `environment` field, so the declaration does NOT
// survive into ActionRunJob.WorkflowPayload and cannot be read back from it. If this test
// ever fails, upstream has added the field and JobEnvironment can read the payload directly
// instead of re-reading the workflow file.
func TestEnvironmentIsAbsentFromWorkflowPayload(t *testing.T) {
	workflows, err := jobparser.Parse([]byte(deployWorkflow))
	require.NoError(t, err)
	require.NotEmpty(t, workflows)

	for _, wf := range workflows {
		payload, err := wf.Marshal()
		require.NoError(t, err)
		env, err := ParseJobEnvironment(payload, "deploy-prod")
		require.NoError(t, err)
		assert.Empty(t, env, "payload still carries no environment; revisit JobEnvironment if this changes")
	}
}
