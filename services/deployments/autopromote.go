// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"context"
	"errors"
	"fmt"

	deployments_model "gitea.dev/models/deployments"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/log"
	"gitea.dev/modules/reqctx"
	"gitea.dev/modules/timeutil"

	"xorm.io/builder"
)

// AutoPromote deploys releaseTag into every environment of repo whose auto_promote is set and
// whose depends_on are all live with releaseTag, because environment just reported success —
// the notifier calls it after every deployment success event.
//
// It never promotes the same (environment, release) pair twice: a deployment row already
// existing for it, waiting, dispatched or finished, is enough to skip it.
func AutoPromote(ctx context.Context, repo *repo_model.Repository, triggeredBy *user_model.User, environment, releaseTag string) error {
	environment = deployments_model.NormalizeEnvironmentName(environment)
	candidates, _, err := deployments_model.FindEnvironments(ctx,
		builder.Eq{"auto_promote": true}.And(builder.In("repo_id", repo.ID, deployments_model.DefaultsRepoID)),
		"id ASC", 0, 0)
	if err != nil {
		return err
	}
	doer := triggeredBy
	if doer == nil {
		doer = user_model.NewGhostUser()
	}
	for _, env := range candidates {
		if !dependsOnNormalized(env, environment) {
			continue // this success is not one of env's dependencies; nothing here changed for it
		}
		if err := autoPromoteOne(ctx, repo, doer, env, environment, releaseTag); err != nil {
			log.Error("deployments: auto-promote %s to %s: %v", releaseTag, env.Name, err)
		}
	}
	return nil
}

func dependsOnNormalized(env *deployments_model.Environment, environment string) bool {
	for _, raw := range env.DependsOn {
		if deployments_model.NormalizeEnvironmentName(raw) == environment {
			return true
		}
	}
	return false
}

// autoPromoteOne handles one candidate environment: skip if it already has a deployment of
// this release, skip if any dependency has not held it live, otherwise dispatch through
// Promote and record the auto_promoted audit event alongside whatever Promote itself wrote.
func autoPromoteOne(ctx context.Context, repo *repo_model.Repository, doer *user_model.User, env *deployments_model.Environment, fromEnvironment, releaseTag string) error {
	exists, err := deployments_model.DeploymentExists(ctx, repo.ID, env.Name, releaseTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	events, err := promotionEvents(ctx, repo.ID, env.Name, env.DependsOn, releaseTag)
	if err != nil {
		return err
	}
	for _, dep := range env.DependsOn {
		if _, state := EvaluateDependencies([]string{dep}, releaseTag, events); state != PredecessorLive {
			return nil // not every dependency is live yet; a later success event tries again
		}
	}

	raw, finished := reqctx.NewRequestContext(ctx, "deployments: auto-promote")
	defer finished()
	reqCtx, ok := raw.(reqctx.RequestContext)
	if !ok {
		return errors.New("could not build a request context")
	}

	promotion, promoteErr := Promote(reqCtx, PromotionRequest{
		Repo: repo, Doer: doer, IsRepoAdmin: true, Environment: env.Name, ReleaseTag: releaseTag, Confirm: true,
	})
	if promoteErr != nil {
		return promoteErr
	}
	if promotion.RunID == 0 {
		// Refused (a prerelease into a releases_only environment) or checks_failed: Promote
		// created nothing, so there is nothing for auto_promote to be credited with.
		return nil
	}
	// auto_promoted records that the deployment above was created by the auto_promote column
	// rather than a person, so it is written only once Promote has actually created one —
	// never on a failed, refused or checks-failed attempt, which left nothing to attribute.
	return deployments_model.AppendAuditEvent(ctx, &deployments_model.AuditEvent{
		Event: deployments_model.AuditAutoPromoted, RepoID: repo.ID, Environment: env.Name, ReleaseTag: releaseTag,
		RunID: promotion.RunID, ActorID: doer.ID, ActorLogin: doer.Name, Source: deployments_model.SourceNotifier,
		Reason: fmt.Sprintf("auto-promoted from %s because every environment %s depends on held %s live",
			fromEnvironment, env.Name, releaseTag),
		OccurredUnix: timeutil.TimeStampNow(),
	})
}
