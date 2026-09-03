// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"fmt"
	"strings"

	"gitea.dev/actionslib/pkg/model"
	deployments_model "gitea.dev/models/deployments"
	hub_model "gitea.dev/models/hub"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/git"
	"gitea.dev/modules/log"
	"gitea.dev/modules/reqctx"
	"gitea.dev/modules/timeutil"
	actions_service "gitea.dev/services/actions"

	"xorm.io/builder"
)

// PredecessorState is what the predecessor environment has done with the release being
// deployed. It is read from the append-only log by the same projection the deployment matrix renders, so
// the confirm step and the cell can never disagree about what happened.
type PredecessorState string

const (
	// PredecessorNone means the environment declares no predecessor; there is no sequence.
	PredecessorNone PredecessorState = "none"
	// PredecessorNever means the predecessor has never held this release.
	PredecessorNever PredecessorState = "never"
	// PredecessorHeld means it held the release previously; something else is live there now.
	PredecessorHeld PredecessorState = "held"
	// PredecessorLive means the release is currently live in the predecessor.
	PredecessorLive PredecessorState = "live"
)

// Outcome is what the sequence rule decided.
type Outcome string

const (
	OutcomeProceed  Outcome = "proceed"
	OutcomeWarn     Outcome = "warn"
	OutcomeOverride Outcome = "override"
	OutcomeRefuse   Outcome = "refuse"
)

// deployWorkflowSuffix completes the convention deployWorkflowPrefix opens: one workflow
// file per environment, named for the environment it deploys to.
const deployWorkflowSuffix = ".yaml"

// WorkflowIDForEnvironment names the workflow a deploy to this environment dispatches. It is
// the inverse of EnvironmentFromWorkflowID, so a run this dispatches is recorded against the
// environment it was dispatched for — one convention, read in both directions.
func WorkflowIDForEnvironment(environment string) string {
	environment = deployments_model.NormalizeEnvironmentName(environment)
	if environment == "" {
		return ""
	}
	return deployWorkflowPrefix + environment + deployWorkflowSuffix
}

// AcceptsRelease reports whether an environment is offered a release of this kind.
//
// An environment takes anything unless it has asked for finished builds only, which is one
// column on its own row: the fork knows no environment names, so it cannot decide this from
// a name. A full release reaches every environment — the rule exists to keep an unfinished
// build out of the environments an operator has named, not to keep a finished one out.
func AcceptsRelease(env *deployments_model.Environment, isPrerelease bool) bool {
	return !isPrerelease || env == nil || !env.ReleasesOnly
}

// EvaluateDependencies reduces the log to what the sequence rule asks of it: has every
// environment this one depends on held this release, and does it hold it now.
//
// With one dependency this is exactly the old predecessor check. With several, every one of
// them must hold the release for the sequence to be satisfied: the first one that never has
// decides the state, and is the dependency named alongside it; once every dependency has held
// it, the last one's own current state (held or live) is what is reported.
//
// It is pure and reuses ProjectCells, so the confirm step reads the release's history
// through the same projection the deployment matrix draws it with.
func EvaluateDependencies(dependsOn []string, releaseTag string, events []Event) (dependency string, state PredecessorState) {
	deps := make([]string, 0, len(dependsOn))
	for _, raw := range dependsOn {
		if dep := deployments_model.NormalizeEnvironmentName(raw); dep != "" {
			deps = append(deps, dep)
		}
	}
	if len(deps) == 0 {
		return "", PredecessorNone
	}
	for _, dep := range deps {
		cells := ProjectCells([]string{dep}, []string{releaseTag}, events, nil)
		row := cells[releaseTag]
		if len(row) == 0 || row[0].Successes == 0 {
			return dep, PredecessorNever
		}
		dependency = dep
		state = PredecessorHeld
		if row[0].State == CellLive {
			state = PredecessorLive
		}
	}
	return dependency, state
}

// Decision is the sequence rule's answer. It is data rather than a boolean because the
// confirm step has to render the warning, and the override has to know a reason is owed.
type Decision struct {
	Outcome                Outcome          `json:"outcome"`
	PredecessorState       PredecessorState `json:"predecessor_state"`
	Message                string           `json:"message"`
	SuggestedAction        string           `json:"suggested_action"`
	RequiresOverrideReason bool             `json:"requires_override_reason"`
}

// DecidePromotion applies the sequence-rule table. It is pure: the state comes from the log and the
// permission from CanBypassEnvironmentSequence, and neither is re-derived here.
//
// dependency names the environment EvaluateDependencies decided the state on — with one
// dependency the one declared, with several the first that has not held the release.
//
//	require_prior_deployment | dependency held | can bypass | outcome
//	false               | either           | either     | warn when never held, else proceed
//	true                | yes              | either     | proceed
//	true                | no               | no         | refuse
//	true                | no               | yes        | override, with a reason
func DecidePromotion(env *deployments_model.Environment, dependency string, state PredecessorState, canBypass bool) Decision {
	d := Decision{Outcome: OutcomeProceed, PredecessorState: state}
	if env == nil || state != PredecessorNever {
		return d
	}
	name := deployments_model.NormalizeEnvironmentName(env.Name)

	if !env.RequirePriorDeployment {
		// With the flag off the sequence is a warning only, so an environment that has set
		// no policy keeps the behaviour it had before the fork.
		d.Outcome = OutcomeWarn
		d.Message = fmt.Sprintf("%s has never held this release, and %s depends on it", dependency, name)
		d.SuggestedAction = fmt.Sprintf("Deploy to %s first, or continue — %s does not require its dependencies.", dependency, name)
		return d
	}
	if !canBypass {
		d.Outcome = OutcomeRefuse
		d.Message = fmt.Sprintf("%s requires %s to have held this release, and it never has", name, dependency)
		d.SuggestedAction = fmt.Sprintf("Deploy the release to %s first, or ask someone on %s's bypass allowlist to override with a reason.", dependency, name)
		return d
	}
	d.Outcome = OutcomeOverride
	d.RequiresOverrideReason = true
	d.Message = fmt.Sprintf("%s requires %s to have held this release, and it never has; you may override", name, dependency)
	d.SuggestedAction = "Send override_reason saying why the sequence is being bypassed; it is recorded on the audit log."
	return d
}

// ErrPromotionNotFound marks a hub error whose subject does not exist — an environment or a
// release the request named. It is a type rather than a message the caller matches on,
// because matching on message text turns every reworded error into a status change.
type ErrPromotionNotFound struct{ Err *hub_model.Error }

func (e *ErrPromotionNotFound) Error() string { return e.Err.Error() }
func (e *ErrPromotionNotFound) Unwrap() error { return e.Err }

// PromotionRequest is one deploy, asked for. Rolling back is deploying a prior release tag,
// so it composes the identical request and differs only in ReleaseTag — there is no rollback
// path to keep in step with the deploy path.
type PromotionRequest struct {
	Repo           *repo_model.Repository
	Doer           *user_model.User
	IsRepoAdmin    bool
	Environment    string
	ReleaseTag     string
	OverrideReason string
	// Confirm is the second of the two steps. With it false nothing is dispatched and the
	// plan is returned for the caller to show; the step is enforced here rather than in the
	// page, so the CLI cannot skip it either.
	Confirm bool
}

// Promotion is what a deploy request resolves to: everything the confirm step has to name
// before anything is dispatched, and the run once something has been.
type Promotion struct {
	RepoID       int64  `json:"repo_id"`
	RepoFullName string `json:"repo_full_name"`
	Environment  string `json:"environment"`
	ReleaseTag   string `json:"release_tag"`
	IsPrerelease bool   `json:"is_prerelease"`
	// CurrentlyLive is the release live in the target environment right now, empty when
	// nothing has ever succeeded there. The confirm step names it.
	CurrentlyLive string `json:"currently_live"`
	// IsRollback reports that the target release is older than what is live there. Rolling
	// back is deploying a prior release tag — the same action, so this labels the request
	// rather than selecting a different one.
	IsRollback     bool     `json:"is_rollback"`
	DependsOn      []string `json:"depends_on"`
	WorkflowID     string   `json:"workflow_id"`
	Ref            string   `json:"ref"`
	Confirmed      bool     `json:"confirmed"`
	OverrideReason string   `json:"override_reason,omitempty"`
	RunID          int64    `json:"run_id"`
	RunURL         string   `json:"run_url"`
	Decision
}

// PlanPromotion resolves the request against the environment record and the log, and
// dispatches nothing. It is the first of the two steps, and every field the confirm step
// has to name is on the value it returns.
func PlanPromotion(ctx reqctx.RequestContext, req PromotionRequest) (*Promotion, error) {
	if req.Repo == nil {
		return nil, &hub_model.Error{
			Message:         "no repository was named",
			SuggestedAction: "Send repo as owner/name, for example \"gitea/gitea\".",
		}
	}
	environment := deployments_model.NormalizeEnvironmentName(req.Environment)
	if environment == "" {
		return nil, &hub_model.Error{
			Message:         "no environment was named",
			SuggestedAction: "Send environment, for example \"prod\". List /api/deployments/v1/environments to see what exists.",
		}
	}
	if strings.TrimSpace(req.ReleaseTag) == "" {
		return nil, &hub_model.Error{
			Message:         "no release tag was named",
			SuggestedAction: "Send release_tag. A deployment points at a release, never at a version string.",
		}
	}

	env, err := deployments_model.GetEnvironment(ctx, req.Repo.ID, environment)
	if err != nil {
		if hubErr, ok := err.(*hub_model.Error); ok {
			return nil, &ErrPromotionNotFound{Err: hubErr}
		}
		return nil, err
	}
	release, err := repo_model.GetRelease(ctx, req.Repo.ID, req.ReleaseTag)
	if err != nil {
		return nil, &ErrPromotionNotFound{Err: &hub_model.Error{
			Message:         fmt.Sprintf("%s has no release tagged %q", req.Repo.FullName(), req.ReleaseTag),
			SuggestedAction: "List /api/deployments/v1/repos/{owner}/{repo}/releases to see the tags this repository can deploy.",
		}}
	}

	out := &Promotion{
		RepoID:       req.Repo.ID,
		RepoFullName: req.Repo.FullName(),
		Environment:  env.Name,
		ReleaseTag:   release.TagName,
		IsPrerelease: release.IsPrerelease,
		DependsOn:    env.DependsOn,
		WorkflowID:   WorkflowIDForEnvironment(env.Name),
		Ref:          git.RefNameFromTag(release.TagName).String(),
	}

	// A prerelease reaching an environment that takes finished builds only is refused
	// wherever it is asked for, so the CLI is not a path around the rule the deployment matrix applies.
	if !AcceptsRelease(env, release.IsPrerelease) {
		out.Decision = Decision{
			Outcome:          OutcomeRefuse,
			PredecessorState: PredecessorNone,
			Message:          fmt.Sprintf("%s is a prerelease and %s takes finished releases only", release.TagName, env.Name),
			SuggestedAction:  fmt.Sprintf("Deploy it to an environment that accepts prereleases, cut a full release, or clear releases_only on %s.", env.Name),
		}
		return out, nil
	}

	events, err := promotionEvents(ctx, req.Repo.ID, env.Name, env.DependsOn, release.TagName)
	if err != nil {
		return nil, err
	}
	out.CurrentlyLive = liveRelease(ctx, req.Repo.ID, env.Name)
	out.IsRollback = isRollback(ctx, req.Repo.ID, out.CurrentlyLive, release)

	dependency, state := EvaluateDependencies(env.DependsOn, release.TagName, events)
	canBypass := CanBypassEnvironmentSequence(ctx, env, req.Doer, req.IsRepoAdmin)
	out.Decision = DecidePromotion(env, dependency, state, canBypass)
	return out, nil
}

// Promote is the single entry point to a deploy. The grid, the API and the CLI all reach a
// dispatch through it, so there is no path around the sequence rule. Rollback is
// this same call with a prior release tag.
//
// It plans first and dispatches only on an explicit confirm, so nothing is dispatched before
// the caller has been shown the target environment, the release tag and what is live there.
func Promote(ctx reqctx.RequestContext, req PromotionRequest) (*Promotion, error) {
	out, err := PlanPromotion(ctx, req)
	if err != nil {
		return nil, err
	}
	out.OverrideReason = strings.TrimSpace(req.OverrideReason)

	if out.Outcome == OutcomeRefuse || !req.Confirm {
		return out, nil
	}
	if out.RequiresOverrideReason && out.OverrideReason == "" {
		// The override is offered only WITH a reason. Dispatching without one would leave
		// the audit log unable to answer why the sequence was bypassed.
		return out, nil
	}

	// The bypass is recorded BEFORE the dispatch. Someone with the right to override used it
	// at this moment for this reason, and that is true whether or not the dispatch then
	// succeeded; recording it afterwards would lose the record exactly when a failed deploy
	// makes it most worth having.
	if out.Outcome == OutcomeOverride {
		if err := appendPromotionEvent(ctx, req, out, deployments_model.AuditOverridden, out.OverrideReason); err != nil {
			return nil, err
		}
	}

	gitRepo, err := git.OpenRepository(ctx, req.Repo)
	if err != nil {
		return nil, dispatchFailed(ctx, req, out, fmt.Sprintf("could not open %s: %v", req.Repo.FullName(), err),
			"Check the repository still exists on disk and that Gitea can read it.")
	}
	defer gitRepo.Close()

	runID, err := actions_service.DispatchActionWorkflow(ctx, req.Doer, req.Repo, gitRepo,
		out.WorkflowID, out.Ref, 0, func(*model.WorkflowDispatch, map[string]any) error { return nil })
	if err != nil {
		return nil, dispatchFailed(ctx, req, out,
			fmt.Sprintf("dispatching %s at %s failed: %v", out.WorkflowID, out.Ref, err),
			fmt.Sprintf("Add %s with a workflow_dispatch trigger on the branch the tag points at, and check the Actions unit is enabled.",
				out.WorkflowID))
	}
	out.RunID = runID
	out.Confirmed = true
	// The same string ActionRun.HTMLURL builds (models/actions/run.go:81-85), so the URL a
	// promotion reports and the one the notifier writes are the same link.
	out.RunURL = fmt.Sprintf("%s/actions/runs/%d", req.Repo.HTMLURL(ctx), runID)

	// The deployment row and its `requested` event are written by the notifier, which sees
	// this run like any other — one code path, so the deployment matrix is complete by construction.
	return out, nil
}

// dispatchFailed records the failure on the log and returns the error to render. A deploy
// that was authorized and then never started is a fact the log has to keep, or a cell would
// sit at "in progress" over a run that does not exist.
func dispatchFailed(ctx reqctx.RequestContext, req PromotionRequest, out *Promotion, message, action string) error {
	if err := appendPromotionEvent(ctx, req, out, deployments_model.AuditFailed, ""); err != nil {
		log.Error("deployments: record failed dispatch of %s to %s: %v", out.ReleaseTag, out.Environment, err)
	}
	return &hub_model.Error{Message: message, SuggestedAction: action}
}

// appendPromotionEvent puts one event on the same append-only log the deploy itself lands
// on. It is this file's only writer; nothing here updates a row.
func appendPromotionEvent(ctx reqctx.RequestContext, req PromotionRequest, out *Promotion, event, reason string) error {
	actorID, actorLogin := int64(0), ""
	if req.Doer != nil {
		actorID, actorLogin = req.Doer.ID, req.Doer.Name
	}
	return deployments_model.AppendAuditEvent(ctx, &deployments_model.AuditEvent{
		Event:        event,
		OccurredUnix: timeutil.TimeStampNow(),
		ActorID:      actorID,
		ActorLogin:   actorLogin,
		RepoID:       out.RepoID,
		Environment:  out.Environment,
		ReleaseTag:   out.ReleaseTag,
		RunID:        out.RunID,
		RunURL:       out.RunURL,
		Source:       deployments_model.SourceUI,
		Reason:       reason,
	})
}

// promotionEvents reads the log rows the dependency evaluation needs: this release, in the
// target environment and in every environment it depends on.
func promotionEvents(ctx reqctx.RequestContext, repoID int64, environment string, dependsOn []string, releaseTag string) ([]Event, error) {
	names := []string{environment}
	for _, raw := range dependsOn {
		if dep := deployments_model.NormalizeEnvironmentName(raw); dep != "" {
			names = append(names, dep)
		}
	}
	cond := builder.Eq{"repo_id": repoID, "release_tag": releaseTag}.And(builder.In("environment", names))
	rows, err := deployments_model.FindAuditEvents(ctx, cond, "occurred_unix ASC, id ASC", 0)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(rows))
	for _, r := range rows {
		events = append(events, Event{
			ID: r.ID, ReleaseTag: r.ReleaseTag, Environment: r.Environment,
			Event: r.Event, OccurredUnix: int64(r.OccurredUnix), RunID: r.RunID, RunURL: r.RunURL,
		})
	}
	return events, nil
}

// liveRelease names what is live in an environment right now. It is read from the log by the
// grid's own projection, never from a "current" flag a second deploy would have to remember
// to clear.
func liveRelease(ctx reqctx.RequestContext, repoID int64, environment string) string {
	rows, err := deployments_model.FindAuditEvents(ctx,
		builder.Eq{"repo_id": repoID, "environment": environment, "event": deployments_model.AuditSucceeded},
		"occurred_unix DESC, id DESC", 1)
	if err != nil || len(rows) == 0 {
		return ""
	}
	return rows[0].ReleaseTag
}

// isRollback reports whether the target release predates what is live in the environment.
// Release age is the release's own creation time, so nothing parses a version string.
//
// It returns no error: the release named as live can have been deleted since it was
// deployed, and the log keeps that deploy either way. That is history to render, not a
// reason to refuse the next deploy, so an unreadable predecessor release reads as "not a
// rollback" rather than failing the request.
func isRollback(ctx reqctx.RequestContext, repoID int64, currentlyLive string, target *repo_model.Release) bool {
	if currentlyLive == "" || currentlyLive == target.TagName {
		return false
	}
	live, err := repo_model.GetRelease(ctx, repoID, currentlyLive)
	if err != nil {
		return false
	}
	return target.CreatedUnix < live.CreatedUnix
}
