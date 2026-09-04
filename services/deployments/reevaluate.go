// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"context"

	deployments_model "gitea.dev/models/deployments"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/git"
	"gitea.dev/modules/json"
	"gitea.dev/modules/log"
	"gitea.dev/modules/reqctx"

	"xorm.io/builder"
)

// ReevaluateWaiting re-evaluates every deployment in the waiting state at now: one whose
// checks all pass is dispatched, one that turns up a failing check moves to failed, and one
// still waiting is left alone for the next call. It is the hub's own sweeper's sweep
// function, and the notifier's own follow-up to every deploy that succeeds.
func ReevaluateWaiting(ctx context.Context, now int64) error {
	// run_id <= 0 is what tells a checks-pending placeholder apart from a real Actions run
	// that happens to be waiting on concurrency or a manual approval — an ordinary state for
	// a dispatched run, and none of this function's business.
	rows, err := deployments_model.FindDeployments(ctx,
		builder.Eq{"status": deployments_model.DeploymentStatusWaiting}.And(builder.Lte{"run_id": 0}), "id ASC", 0)
	if err != nil {
		return err
	}
	for _, d := range rows {
		reevaluateWaitingDeployment(ctx, d, now)
	}
	return nil
}

// reevaluateWaitingDeployment handles one row. Errors are logged rather than returned: one
// placeholder that cannot be resolved this sweep must not stop every other one from being
// re-evaluated.
func reevaluateWaitingDeployment(ctx context.Context, d *deployments_model.Deployment, now int64) {
	env, err := deployments_model.GetEnvironment(ctx, d.RepoID, d.Environment)
	if err != nil {
		log.Error("deployments: re-evaluate waiting deployment %d: read environment %q of repo %d: %v",
			d.ID, d.Environment, d.RepoID, err)
		return
	}
	checks, err := EvaluateChecks(ctx, CheckContext{
		RepoID: d.RepoID, Env: env, ReleaseTag: d.ReleaseTag, SHA: d.SHA,
		RequestedUnix: int64(d.CreatedUnix), ExcludeDeploymentID: d.ID,
	}, now)
	if err != nil {
		log.Error("deployments: re-evaluate waiting deployment %d: evaluate checks: %v", d.ID, err)
		return
	}

	switch AggregateCheckState(checks) {
	case CheckPass:
		dispatchWaitingDeployment(ctx, d, checks)
	case CheckFail:
		failWaitingDeployment(ctx, d, checks)
	}
	// CheckWait: leave it exactly as it is for the next sweep.
}

// requesterForPlaceholder reads back the user who asked for a waiting deployment, off the
// checks_pending event AppendPlaceholderDeployment's caller recorded under the same RunID. A
// deploy dispatched here is dispatched as that user, the same as if their original request had
// passed every check immediately.
func requesterForPlaceholder(ctx context.Context, d *deployments_model.Deployment) *user_model.User {
	rows, err := deployments_model.FindAuditEvents(ctx, builder.Eq{
		"repo_id": d.RepoID, "environment": d.Environment, "run_id": d.RunID,
		"event": deployments_model.AuditChecksPending,
	}, "id DESC", 1)
	if err == nil && len(rows) > 0 && rows[0].ActorID > 0 {
		if u, uErr := user_model.GetUserByID(ctx, rows[0].ActorID); uErr == nil {
			return u
		}
	}
	return user_model.NewGhostUser()
}

// dispatchWaitingDeployment dispatches the deploy a placeholder was standing in for. The
// placeholder is deleted on a successful dispatch — the notifier appends the row that is this
// deploy's real history once Actions reports the run — and marked failed if the dispatch
// itself could not start, the same failure a live confirm reports.
func dispatchWaitingDeployment(ctx context.Context, d *deployments_model.Deployment, checks []Check) {
	if err := appendChecksAudit(ctx, d, deployments_model.AuditChecksPassed, checks); err != nil {
		log.Error("deployments: record checks_passed for waiting deployment %d: %v", d.ID, err)
	}

	fail := func(reason string, args ...any) {
		log.Error("deployments: dispatch waiting deployment %d: "+reason, append([]any{d.ID}, args...)...)
		if failErr := deployments_model.FailPlaceholderDeployment(ctx, d.ID); failErr != nil {
			log.Error("deployments: mark waiting deployment %d failed: %v", d.ID, failErr)
		}
	}

	repo, err := repo_model.GetRepositoryByID(ctx, d.RepoID)
	if err != nil {
		fail("load repo %d: %v", d.RepoID, err)
		return
	}
	raw, finished := reqctx.NewRequestContext(ctx, "deployments: dispatch waiting deployment")
	defer finished()
	reqCtx, ok := raw.(reqctx.RequestContext)
	if !ok {
		fail("could not build a request context")
		return
	}

	doer := requesterForPlaceholder(ctx, d)
	workflowID := WorkflowIDForEnvironment(d.Environment)
	ref := git.RefNameFromTag(d.ReleaseTag).String()
	if _, _, err := dispatchDeployWorkflow(reqCtx, doer, repo, workflowID, ref); err != nil {
		fail("%v", err)
		return
	}
	if err := deployments_model.DeletePlaceholderDeployment(ctx, d.ID); err != nil {
		log.Error("deployments: delete dispatched placeholder %d: %v", d.ID, err)
	}
}

// failWaitingDeployment moves a placeholder to failed once its checks turn up a fail that
// waiting cannot resolve.
func failWaitingDeployment(ctx context.Context, d *deployments_model.Deployment, checks []Check) {
	if err := deployments_model.FailPlaceholderDeployment(ctx, d.ID); err != nil {
		log.Error("deployments: mark waiting deployment %d failed: %v", d.ID, err)
		return
	}
	if err := appendChecksAudit(ctx, d, deployments_model.AuditChecksFailed, checks); err != nil {
		log.Error("deployments: record checks_failed for waiting deployment %d: %v", d.ID, err)
	}
}

// appendChecksAudit records event against d, with checks serialized as the reason so the
// audit log names exactly what was pending or what failed.
func appendChecksAudit(ctx context.Context, d *deployments_model.Deployment, event string, checks []Check) error {
	reasons, err := json.Marshal(checks)
	if err != nil {
		return err
	}
	return deployments_model.AppendAuditEvent(ctx, &deployments_model.AuditEvent{
		Event: event, RepoID: d.RepoID, Environment: d.Environment, ReleaseTag: d.ReleaseTag,
		SHA: d.SHA, RunID: d.RunID, Source: deployments_model.SourceReconcile, Reason: string(reasons),
	})
}
