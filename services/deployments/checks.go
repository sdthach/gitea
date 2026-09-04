// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"context"
	"fmt"
	"time"

	"gitea.dev/models/db"
	deployments_model "gitea.dev/models/deployments"
	git_model "gitea.dev/models/git"
	"gitea.dev/modules/commitstatus"

	"xorm.io/builder"
)

// CheckState is one pre-deployment check's answer.
type CheckState string

const (
	CheckPass CheckState = "pass"
	CheckWait CheckState = "wait"
	CheckFail CheckState = "fail"
)

// The check names EvaluateChecks reports, in the order it evaluates them.
const (
	CheckReviewers              = "reviewers"
	CheckPriorDeployment        = "prior_deployment"
	CheckReleasesOnly           = "releases_only"
	CheckWaitTimer              = "wait_timer"
	CheckDeploymentWindow       = "deployment_window"
	CheckRequiredStatusContexts = "required_status_contexts"
	CheckExclusiveLock          = "exclusive_lock"
)

// Check is one pre-deployment check's outcome. RetryAt is unix seconds, set only when a wait
// names a time worth retrying at.
type Check struct {
	Name            string     `json:"name"`
	State           CheckState `json:"state"`
	Reason          string     `json:"reason,omitempty"`
	SuggestedAction string     `json:"suggested_action,omitempty"`
	RetryAt         int64      `json:"retry_at,omitempty"`
}

// AggregateCheckState reduces a check list to one verdict: any fail wins over any wait, which
// wins over pass. It is what both Promote and ReevaluateWaiting act on.
func AggregateCheckState(checks []Check) CheckState {
	state := CheckPass
	for _, c := range checks {
		switch c.State {
		case CheckFail:
			return CheckFail
		case CheckWait:
			state = CheckWait
		}
	}
	return state
}

// CheckContext is what EvaluateChecks needs to answer every check. Overridden is set once the
// caller's own sequence decision (services/deployments.DecidePromotion) already granted a
// bypass of require_prior_deployment for this exact request — the prior_deployment check
// defers to that decision rather than holding a deploy a human with the right to override
// already authorized.
type CheckContext struct {
	RepoID        int64
	Env           *deployments_model.Environment
	ReleaseTag    string
	IsPrerelease  bool
	SHA           string
	RequestedUnix int64
	Overridden    bool
	// ExcludeDeploymentID is the deployment row under evaluation, when one already exists —
	// set by ReevaluateWaiting for the waiting placeholder it is re-checking, and by the
	// GET /deployments/{id}/checks endpoint. exclusiveLockCheck must not count that row
	// against itself, or a deploy holding exclusive_lock could never clear its own lock.
	ExcludeDeploymentID int64
}

// EvaluateChecks runs every pre-deployment check for env against release/sha, in the order
// the deployment matrix and the checks endpoint both render them.
func EvaluateChecks(ctx context.Context, cc CheckContext, now int64) ([]Check, error) {
	env := cc.Env
	checks := make([]Check, 0, 7)

	reviewers, err := reviewersCheck(ctx, cc.RepoID, env, cc.ReleaseTag)
	if err != nil {
		return nil, err
	}
	checks = append(checks, reviewers)

	events, err := promotionEvents(ctx, cc.RepoID, env.Name, env.DependsOn, cc.ReleaseTag)
	if err != nil {
		return nil, err
	}
	checks = append(checks, priorDeploymentCheck(env, cc.ReleaseTag, events, cc.Overridden))
	checks = append(checks, releasesOnlyCheck(env, cc.ReleaseTag, cc.IsPrerelease))
	checks = append(checks, waitTimerCheck(env, cc.RequestedUnix, now))
	checks = append(checks, deploymentWindowCheck(env, now))

	contexts, err := requiredStatusContextsCheck(ctx, cc.RepoID, env, cc.SHA, now)
	if err != nil {
		return nil, err
	}
	checks = append(checks, contexts)

	lock, err := exclusiveLockCheck(ctx, cc.RepoID, env, cc.ExcludeDeploymentID)
	if err != nil {
		return nil, err
	}
	checks = append(checks, lock)

	return checks, nil
}

// reviewersCheck reuses the existing review policy: it looks at whichever run this
// environment has most recently recorded for releaseTag and asks whether that run's review
// hold, if any, is still pending. A release that has never been dispatched here has nothing
// held yet, so it passes — the review gate itself still holds the job once dispatch happens,
// unchanged by this check.
func reviewersCheck(ctx context.Context, repoID int64, env *deployments_model.Environment, releaseTag string) (Check, error) {
	if env.ReviewPolicy == "" || env.ReviewPolicy == deployments_model.PolicyNone {
		return Check{Name: CheckReviewers, State: CheckPass}, nil
	}
	rows, err := deployments_model.FindDeployments(ctx,
		builder.Eq{"repo_id": repoID, "environment": env.Name, "release_tag": releaseTag}, "created_unix DESC", 1)
	if err != nil {
		return Check{}, err
	}
	if len(rows) == 0 {
		return Check{Name: CheckReviewers, State: CheckPass}, nil
	}
	holds, _, err := deployments_model.FindReviews(ctx,
		builder.Eq{"repo_id": repoID, "environment": env.Name, "run_id": rows[0].RunID}, "id ASC", 0, 0)
	if err != nil {
		return Check{}, err
	}
	for _, h := range holds {
		votes, err := deployments_model.VotesForReview(ctx, h)
		if err != nil {
			return Check{}, err
		}
		state, _ := deployments_model.ProjectReviewState(env.ReviewPolicy, env.RequiredReviewers, h.RequesterID, votes)
		switch state {
		case deployments_model.ReviewRejected:
			return Check{
				Name: CheckReviewers, State: CheckFail,
				Reason:          fmt.Sprintf("the deploy of %s to %s was rejected", releaseTag, env.Name),
				SuggestedAction: "Dispatch the deploy again if the rejection should not stand.",
			}, nil
		case deployments_model.ReviewPending:
			return Check{
				Name: CheckReviewers, State: CheckWait,
				Reason:          fmt.Sprintf("%s needs %d review(s) under its %s policy", env.Name, env.RequiredReviewers, env.ReviewPolicy),
				SuggestedAction: "Ask an approver to review the held deploy.",
			}, nil
		}
	}
	return Check{Name: CheckReviewers, State: CheckPass}, nil
}

// priorDeploymentCheck applies EvaluateDependencies to every depends_on entry, requiring each
// to be currently live with releaseTag. It only gates when RequirePriorDeployment is set —
// with it unset, the sequence is a warning DecidePromotion already renders, not a hold here —
// and it defers entirely once a human has already been granted an override for this request.
func priorDeploymentCheck(env *deployments_model.Environment, releaseTag string, events []Event, overridden bool) Check {
	if overridden || !env.RequirePriorDeployment || len(env.DependsOn) == 0 {
		return Check{Name: CheckPriorDeployment, State: CheckPass}
	}
	for _, dep := range env.DependsOn {
		depName, state := EvaluateDependencies([]string{dep}, releaseTag, events)
		if state != PredecessorLive {
			return Check{
				Name: CheckPriorDeployment, State: CheckWait,
				Reason:          fmt.Sprintf("%s has not held %s live", depName, releaseTag),
				SuggestedAction: fmt.Sprintf("Deploy %s to %s first.", releaseTag, depName),
			}
		}
	}
	return Check{Name: CheckPriorDeployment, State: CheckPass}
}

// releasesOnlyCheck reuses AcceptsRelease: a prerelease reaching a releases_only environment
// fails rather than waits, since no amount of waiting turns a prerelease into a full one.
func releasesOnlyCheck(env *deployments_model.Environment, releaseTag string, isPrerelease bool) Check {
	if AcceptsRelease(env, isPrerelease) {
		return Check{Name: CheckReleasesOnly, State: CheckPass}
	}
	return Check{
		Name: CheckReleasesOnly, State: CheckFail,
		Reason:          fmt.Sprintf("%s is a prerelease and %s takes finished releases only", releaseTag, env.Name),
		SuggestedAction: fmt.Sprintf("Deploy a full release, or clear releases_only on %s.", env.Name),
	}
}

// waitTimerCheck holds every deploy for wait_minutes after it was requested. Zero, the
// default, never holds.
func waitTimerCheck(env *deployments_model.Environment, requestedUnix, now int64) Check {
	if env.WaitMinutes <= 0 {
		return Check{Name: CheckWaitTimer, State: CheckPass}
	}
	retryAt := requestedUnix + int64(env.WaitMinutes)*60
	if now >= retryAt {
		return Check{Name: CheckWaitTimer, State: CheckPass}
	}
	return Check{
		Name: CheckWaitTimer, State: CheckWait,
		Reason:          fmt.Sprintf("%s holds every deploy for %d minute(s)", env.Name, env.WaitMinutes),
		SuggestedAction: "Wait for the timer, or ask an operator to lower wait_minutes.",
		RetryAt:         retryAt,
	}
}

// windowOpen reports whether now falls inside window, evaluated in window's own timezone. A
// nil window, or one with DaysMask zero, is always open.
//
// FromMinute > ToMinute is an overnight window: it opens at FromMinute on a masked day and
// stays open past midnight until ToMinute the following calendar day, whichever day that is —
// the mask names the day the window STARTS, not every day it is open on.
func windowOpen(now int64, window *deployments_model.DeployWindow) (bool, error) {
	if window == nil || window.DaysMask == 0 {
		return true, nil
	}
	loc, err := time.LoadLocation(window.Timezone)
	if err != nil {
		return false, err
	}
	t := time.Unix(now, 0).In(loc)
	minute := t.Hour()*60 + t.Minute()
	if window.FromMinute <= window.ToMinute {
		if window.DaysMask&(1<<uint(t.Weekday())) == 0 {
			return false, nil
		}
		return minute >= window.FromMinute && minute < window.ToMinute, nil
	}
	// Overnight: open either in today's evening leg (today started the window) or in this
	// morning's leg carried over from a window yesterday started.
	if window.DaysMask&(1<<uint(t.Weekday())) != 0 && minute >= window.FromMinute {
		return true, nil
	}
	yesterday := t.AddDate(0, 0, -1).Weekday()
	return window.DaysMask&(1<<uint(yesterday)) != 0 && minute < window.ToMinute, nil
}

// NextOpening returns the next unix time at or after now that windowOpen would report open,
// scanning day by day so a daylight-saving transition on the way there shifts the wall-clock
// answer, never the zone-relative one. It looks at most a week ahead, which the mask being
// 1..127 guarantees is enough to find a match.
func NextOpening(now int64, window *deployments_model.DeployWindow) (int64, error) {
	if window == nil || window.DaysMask == 0 {
		return now, nil
	}
	loc, err := time.LoadLocation(window.Timezone)
	if err != nil {
		return 0, err
	}
	t := time.Unix(now, 0).In(loc)
	for offset := 0; offset <= 7; offset++ {
		day := t.AddDate(0, 0, offset)
		if window.DaysMask&(1<<uint(day.Weekday())) == 0 {
			continue
		}
		opens := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc).
			Add(time.Duration(window.FromMinute) * time.Minute)
		if opens.Before(t) {
			continue // this day's window already opened (and, since we are here, closed) before now
		}
		return opens.Unix(), nil
	}
	return now, nil // DaysMask 1..127 always matches within 7 days; unreached in practice
}

// deploymentWindowCheck wraps windowOpen and NextOpening for the aggregate.
func deploymentWindowCheck(env *deployments_model.Environment, now int64) Check {
	open, err := windowOpen(now, env.DeployWindow)
	if err != nil {
		return Check{
			Name: CheckDeploymentWindow, State: CheckFail,
			Reason:          fmt.Sprintf("deploy_window timezone is invalid: %v", err),
			SuggestedAction: "Fix the environment's deploy_window timezone to an IANA zone name.",
		}
	}
	if open {
		return Check{Name: CheckDeploymentWindow, State: CheckPass}
	}
	retryAt, err := NextOpening(now, env.DeployWindow)
	if err != nil {
		retryAt = 0
	}
	return Check{
		Name: CheckDeploymentWindow, State: CheckWait,
		Reason:          env.Name + " only deploys inside its configured window",
		SuggestedAction: "Wait for the window to open, or widen deploy_window.",
		RetryAt:         retryAt,
	}
}

// requiredStatusContextRetrySeconds is how soon a missing or pending required context is
// worth checking again — short, because a status report can land at any moment and this is
// only ever a suggestion for when to poll, not a hold with its own timer.
const requiredStatusContextRetrySeconds = 60

// requiredContextsCheck is windowOpen's counterpart for commit statuses: pure over an already
// -read status list, so the failure and success tables need no database. A context that has
// not reported yet, or has reported pending, WAITS — it may still turn green. Only a report
// of failure or error is terminal.
func requiredContextsCheck(required []string, statuses []*git_model.CommitStatus, now int64) Check {
	if len(required) == 0 {
		return Check{Name: CheckRequiredStatusContexts, State: CheckPass}
	}
	latest := make(map[string]commitstatus.CommitStatusState, len(statuses))
	for _, s := range statuses {
		latest[s.Context] = s.State
	}
	for _, want := range required {
		state, ok := latest[want]
		if !ok || state == commitstatus.CommitStatusPending {
			return Check{
				Name: CheckRequiredStatusContexts, State: CheckWait,
				Reason:          fmt.Sprintf("%q has not reported success on this commit yet", want),
				SuggestedAction: fmt.Sprintf("Wait for %q to succeed, or remove it from required_status_contexts.", want),
				RetryAt:         now + requiredStatusContextRetrySeconds,
			}
		}
		if state != commitstatus.CommitStatusSuccess {
			return Check{
				Name: CheckRequiredStatusContexts, State: CheckFail,
				Reason:          fmt.Sprintf("%q reports %s, not success", want, state),
				SuggestedAction: fmt.Sprintf("Wait for %q to succeed on the release commit.", want),
			}
		}
	}
	return Check{Name: CheckRequiredStatusContexts, State: CheckPass}
}

// requiredStatusContextsCheck reads the release commit's latest statuses and applies
// requiredContextsCheck.
func requiredStatusContextsCheck(ctx context.Context, repoID int64, env *deployments_model.Environment, sha string, now int64) (Check, error) {
	if len(env.RequiredStatusContexts) == 0 {
		return Check{Name: CheckRequiredStatusContexts, State: CheckPass}, nil
	}
	if sha == "" {
		return Check{
			Name: CheckRequiredStatusContexts, State: CheckFail,
			Reason:          "the release names no commit to check statuses against",
			SuggestedAction: "Deploy a release that points at a resolvable commit.",
		}, nil
	}
	statuses, err := git_model.GetLatestCommitStatus(ctx, repoID, sha, db.ListOptionsAll)
	if err != nil {
		return Check{}, err
	}
	return requiredContextsCheck(env.RequiredStatusContexts, statuses, now), nil
}

// exclusiveLockCheck holds a deploy while another is already waiting or running on the same
// environment. excludeID leaves the deployment under evaluation itself out of that search —
// see CheckContext.ExcludeDeploymentID.
func exclusiveLockCheck(ctx context.Context, repoID int64, env *deployments_model.Environment, excludeID int64) (Check, error) {
	if !env.ExclusiveLock {
		return Check{Name: CheckExclusiveLock, State: CheckPass}, nil
	}
	busy, err := deployments_model.ActiveDeploymentExists(ctx, repoID, env.Name, excludeID)
	if err != nil {
		return Check{}, err
	}
	if !busy {
		return Check{Name: CheckExclusiveLock, State: CheckPass}, nil
	}
	return Check{
		Name: CheckExclusiveLock, State: CheckWait,
		Reason:          env.Name + " already has a deployment in progress or waiting",
		SuggestedAction: "Wait for the other deployment to finish.",
	}, nil
}
