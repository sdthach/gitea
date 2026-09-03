// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"context"
	"errors"
	"fmt"
	"strings"

	actions_model "gitea.dev/models/actions"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/modules/actions"
	"gitea.dev/modules/git"

	"go.yaml.in/yaml/v4"
)

// ErrNoWorkflowSource is returned when a run does not record where its workflow file came
// from, so the job's declared environment cannot be read.
var ErrNoWorkflowSource = errors.New("run records no workflow source commit")

// workflowEnvelope is the fork's own minimal view of a workflow file: enough to read each
// job's `environment:` and nothing else.
//
// The environment CANNOT be read back from ActionRunJob.WorkflowPayload. Measured at the
// pin: jobparser.Job declares no `environment` field, WorkflowPayload is
// SingleWorkflow.Marshal() of that struct, and a probe confirmed `environment: prod` is
// absent from the marshalled payload. The declaration therefore has to be re-read from the
// workflow file at (WorkflowRepoID, WorkflowCommitSHA), which the run records.
type workflowEnvelope struct {
	Jobs map[string]struct {
		Environment yaml.Node `yaml:"environment"`
	} `yaml:"jobs"`
}

// ParseJobEnvironment reads the environment a job declares out of raw workflow YAML.
// It accepts both `environment: prod` and `environment: {name: prod}`.
//
// An environment given as an unevaluated expression resolves to "" — no environment — so a
// workflow cannot reach an environment-scoped secret through a value the fork has not
// resolved. Failing closed is the only safe reading of an unknown value here.
func ParseJobEnvironment(workflowYAML []byte, jobID string) (string, error) {
	var wf workflowEnvelope
	if err := yaml.Unmarshal(workflowYAML, &wf); err != nil {
		return "", fmt.Errorf("parse workflow: %w", err)
	}
	job, ok := wf.Jobs[jobID]
	if !ok {
		return "", nil
	}
	node := job.Environment
	switch node.Kind {
	case 0:
		return "", nil
	case yaml.ScalarNode:
		return normalizeDeclaredEnvironment(node.Value), nil
	case yaml.MappingNode:
		var named struct {
			Name string `yaml:"name"`
		}
		if err := node.Decode(&named); err != nil {
			return "", fmt.Errorf("parse job %q environment: %w", jobID, err)
		}
		return normalizeDeclaredEnvironment(named.Name), nil
	}
	return "", nil
}

func normalizeDeclaredEnvironment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "${{") {
		return ""
	}
	return NormalizeEnvironmentName(value)
}

// JobEnvironment resolves the environment a job declares, by re-reading its workflow file
// from the commit the run recorded.
func JobEnvironment(ctx context.Context, job *actions_model.ActionRunJob) (string, error) {
	if err := job.LoadRun(ctx); err != nil {
		return "", err
	}
	run := job.Run
	if run.WorkflowRepoID == 0 || run.WorkflowCommitSHA == "" {
		return "", ErrNoWorkflowSource
	}
	sourceRepo, err := repo_model.GetRepositoryByID(ctx, run.WorkflowRepoID)
	if err != nil {
		return "", err
	}
	gitRepo, err := git.OpenRepository(ctx, sourceRepo)
	if err != nil {
		return "", err
	}
	defer gitRepo.Close()

	commit, err := gitRepo.GetCommit(ctx, run.WorkflowCommitSHA)
	if err != nil {
		return "", err
	}
	_, entries, err := actions.ListWorkflows(ctx, gitRepo, commit)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Name() != run.WorkflowID {
			continue
		}
		content, err := actions.GetContentFromEntry(ctx, gitRepo, entry)
		if err != nil {
			return "", err
		}
		return ParseJobEnvironment(content, job.JobID)
	}
	return "", fmt.Errorf("workflow %q not found at %s: %w", run.WorkflowID, run.WorkflowCommitSHA, ErrNoWorkflowSource)
}
