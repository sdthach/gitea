// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"
	"path"
	"strings"

	actions_model "gitea.dev/models/actions"
	delivery_model "gitea.dev/models/delivery"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/git"
	"gitea.dev/modules/log"
	"gitea.dev/modules/timeutil"
	"gitea.dev/services/notify"
)

// deployWorkflowPrefix is the D4 convention: one workflow file per environment, named for
// the environment it deploys to. It is what makes workflow_id a proxy for environment, so
// Gitea's own filtered run list is the per-environment deployment history.
const deployWorkflowPrefix = "deploy-"

// EnvironmentFromWorkflowID reads the environment out of a deploy workflow's file name
// (D4). A workflow that is not a deploy workflow resolves to "" and is not recorded, so
// adding the fork records nothing for a repository that has no deploy workflow.
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

// EventForRunStatus maps a run's state to the audit event it records (E5). An unmapped
// state records nothing rather than a guess: the log is evidence, so an event it cannot
// justify must not appear in it.
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
// reaches no database and no network — so every state and every trigger is testable in
// isolation (J5, J10).
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
		// A deployment points at a release tag (D1). A deploy dispatched against a branch
		// carries no release identity, so there is nothing to place in the grid.
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
		// One code path records every deploy, whether it was started from the grid, from
		// Gitea's own UI, or by a push, so the grid is complete by construction rather
		// than by reconciliation (E11).
		Source: delivery_model.SourceNotifier,
	}
	return deployment, audit, true
}

// notifier captures deployment events from Gitea's own internal notifier (E2). There is no
// webhook receiver, no signature validation and no delivery retries: the events never leave
// the process (E1, E2).
type notifier struct {
	notify.NullNotifier
}

// WorkflowRunStatusUpdate records a deploy run's state change.
//
// It is the fork's whole capture surface. Registering on Gitea's own notifier registry is
// what lets the fork observe every run without editing an upstream file (F2).
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
}

// NewNotifier builds the fork's notifier. It is exported so a test can register it against
// a test engine without going through Init.
func NewNotifier() notify.Notifier { return &notifier{} }

// Init mounts the fork: it runs the hub's own migrations and seeds, then registers the
// deployment notifier. It is what routers/init.go's single hub-mount spoke calls (F2, F3,
// F6, M5).
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
