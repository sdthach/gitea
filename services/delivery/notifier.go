// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"
	"fmt"
	"path"
	"strings"

	actions_model "gitea.dev/models/actions"
	delivery_model "gitea.dev/models/delivery"
	git_model "gitea.dev/models/git"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/commitstatus"
	"gitea.dev/modules/git"
	"gitea.dev/modules/log"
	"gitea.dev/modules/timeutil"
	"gitea.dev/services/notify"
)

// deployWorkflowPrefix: one workflow file per environment, named for the environment it
// deploys to, so Gitea's own filtered run list is the per-environment deployment history.
const deployWorkflowPrefix = "deploy-"

// EnvironmentFromWorkflowID reads the environment out of a deploy workflow's file name.
// A non-deploy workflow resolves to "" and is not recorded.
func EnvironmentFromWorkflowID(workflowID string) string {
	name := path.Base(strings.TrimSpace(workflowID))
	if !strings.HasPrefix(name, deployWorkflowPrefix) {
		return ""
	}
	name = strings.TrimPrefix(name, deployWorkflowPrefix)
	for _, ext := range []string{".yaml", ".yml"} {
		if trimmed, found := strings.CutSuffix(name, ext); found {
			return delivery_model.NormalizeEnvironmentName(trimmed)
		}
	}
	// A name without a workflow extension is not a workflow file name; refusing it keeps
	// "deploy-anything" from inventing an environment.
	return ""
}

// EventForRunStatus maps a run's state to the audit event it records. An unmapped state
// records nothing rather than a guess.
func EventForRunStatus(status actions_model.Status) string {
	switch status {
	case actions_model.StatusWaiting, actions_model.StatusBlocked:
		return delivery_model.AuditRequested
	case actions_model.StatusRunning:
		return delivery_model.AuditStarted
	case actions_model.StatusSuccess:
		return delivery_model.AuditSucceeded
	case actions_model.StatusFailure:
		return delivery_model.AuditFailed
	case actions_model.StatusCancelled, actions_model.StatusSkipped:
		return delivery_model.AuditCancelled
	}
	return ""
}

// RecordsForRun converts a run state change into the rows it records. It is pure — it
// reaches no database and no network — so every state and trigger is testable in isolation.
//
// The deployment row and the audit row are returned together because they describe the same
// observation: the deployment is appended once per run, and the audit row once per state
// change. ok is false when the run is not a deploy, which is most runs.
func RecordsForRun(repo *repo_model.Repository, sender *user_model.User, run *actions_model.ActionRun) (*delivery_model.Deployment, *delivery_model.AuditEvent, bool) {
	if repo == nil || run == nil {
		return nil, nil, false
	}
	environment := EnvironmentFromWorkflowID(run.WorkflowID)
	if environment == "" {
		return nil, nil, false
	}
	event := EventForRunStatus(run.Status)
	if event == "" {
		return nil, nil, false
	}

	ref := git.RefName(run.Ref)
	releaseTag, branch := "", ""
	switch {
	case ref.IsTag():
		releaseTag = ref.TagName()
	case ref.IsBranch():
		branch = ref.BranchName()
	}
	if releaseTag == "" {
		// A deploy dispatched against a branch carries no release identity.
		return nil, nil, false
	}

	run.Repo = repo
	runURL := run.HTMLURL()

	actorID, actorLogin := int64(0), ""
	if sender != nil {
		actorID, actorLogin = sender.ID, sender.Name
	} else if run.TriggerUser != nil {
		actorID, actorLogin = run.TriggerUser.ID, run.TriggerUser.Name
	}

	occurred := run.Updated
	if occurred == 0 {
		occurred = timeutil.TimeStampNow()
	}

	deployment := &delivery_model.Deployment{
		RepoID:      repo.ID,
		Environment: environment,
		ReleaseTag:  releaseTag,
		SHA:         run.CommitSHA,
		Branch:      branch,
		RunID:       run.ID,
		RunURL:      runURL,
		Status:      run.Status.String(),
	}
	audit := &delivery_model.AuditEvent{
		Event:        event,
		OccurredUnix: occurred,
		ActorID:      actorID,
		ActorLogin:   actorLogin,
		RepoID:       repo.ID,
		Environment:  environment,
		ReleaseTag:   releaseTag,
		SHA:          run.CommitSHA,
		Branch:       branch,
		RunID:        run.ID,
		RunURL:       runURL,
		// One code path records every deploy, whether started from the grid, from Gitea's
		// own UI, or by a push, so the grid is complete by construction.
		Source: delivery_model.SourceNotifier,
	}
	return deployment, audit, true
}

// notifier captures deployment events from Gitea's own internal notifier. There is no
// webhook receiver, no signature validation and no delivery retries: the events never leave
// the process.
type notifier struct {
	notify.NullNotifier
}

// WorkflowRunStatusUpdate records a deploy run's state change.
//
// It is the fork's whole capture surface. Registering on Gitea's own notifier registry
// lets the fork observe every run without editing an upstream file.
func (n *notifier) WorkflowRunStatusUpdate(ctx context.Context, repo *repo_model.Repository, sender *user_model.User, run *actions_model.ActionRun) {
	deployment, audit, ok := RecordsForRun(repo, sender, run)
	if !ok {
		return
	}
	if err := delivery_model.AppendDeployment(ctx, deployment); err != nil {
		log.Error("delivery: record deployment of %s to %s (run %d): %v — the grid will not show this deploy; check the database is reachable and re-run the workflow",
			deployment.ReleaseTag, deployment.Environment, deployment.RunID, err)
	}
	if err := delivery_model.AppendAuditEvent(ctx, audit); err != nil {
		log.Error("delivery: record %s event for %s to %s (run %d): %v — the audit log is incomplete for this deploy; check the database is reachable",
			audit.Event, audit.ReleaseTag, audit.Environment, audit.RunID, err)
	}

	// Write a commit status so the SHA's status page shows the deploy outcome.
	sha, shaErr := git.NewIDFromString(deployment.SHA)
	if shaErr == nil {
		actor := sender
		if actor == nil {
			actor = user_model.NewGhostUser()
		}
		csErr := git_model.NewCommitStatus(ctx, git_model.NewCommitStatusOptions{
			Repo:    repo,
			Creator: actor,
			SHA:     sha,
			CommitStatus: &git_model.CommitStatus{
				State:       mapRunStatusToCommitState(run.Status),
				Context:     "deploy/" + deployment.Environment,
				Description: fmt.Sprintf("%s %s to %s", deployment.ReleaseTag, run.Status.String(), deployment.Environment),
				TargetURL:   deployment.RunURL,
			},
		})
		if csErr != nil {
			log.Error("delivery: commit status for %s to %s: %v", deployment.SHA, deployment.Environment, csErr)
		}
	}

	// Tag the commit so `git log --oneline deployed/env` shows the latest deploy.
	if run.Status.IsSuccess() {
		tagName := "deployed/" + deployment.Environment
		gitRepo, err := git.OpenRepository(ctx, repo)
		if err == nil {
			defer gitRepo.Close()
			if err := gitRepo.CreateTag(ctx, tagName, deployment.SHA); err != nil {
				log.Error("delivery: tag %s at %s: %v", tagName, deployment.SHA, err)
			}
		} else {
			log.Error("delivery: open repo %s for tag: %v", repo.FullName(), err)
		}
	}
}

func mapRunStatusToCommitState(status actions_model.Status) commitstatus.CommitStatusState {
	switch {
	case status.IsSuccess():
		return commitstatus.CommitStatusSuccess
	case status.IsFailure():
		return commitstatus.CommitStatusFailure
	case status.IsCancelled(), status.IsSkipped():
		return commitstatus.CommitStatusError
	default:
		return commitstatus.CommitStatusPending
	}
}

// NewNotifier builds the fork's notifier. It is exported so a test can register it against
// a test engine without going through Init.
func NewNotifier() notify.Notifier { return &notifier{} }

// Init mounts the fork: it runs the hub's own migrations and seeds, then registers the
// deployment notifier.
//
// The notifier is registered here rather than from models/delivery because services/notify
// sits above the models layer; registering from the model package would invert the
// dependency.
func Init(ctx context.Context) error {
	if err := delivery_model.Init(ctx); err != nil {
		return err
	}
	notify.RegisterNotifier(NewNotifier())
	return nil
}
