// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"
	"fmt"

	delivery_model "gitea.dev/models/delivery"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/log"

	"xorm.io/builder"
)

// CanApproveEnvironment reports whether user may approve or reject a deploy held by env
// (F5a, F12).
//
// The approver set DEFAULTS to the users Gitea already permits to dispatch — write on the
// Actions unit, which the caller resolves through Gitea's own permission check and passes
// as canDispatch. Narrowing it to named users and teams is the same opt-in allowlist branch
// protection uses, resolved through the one helper that reads those fields
// (CanBypassEnvironmentSequence), so no gate in this work models permission a second way.
//
// It is fail-CLOSED: every branch that cannot answer returns false.
func CanApproveEnvironment(ctx context.Context, env *delivery_model.Environment, user *user_model.User, isRepoAdmin, canDispatch bool) bool {
	if env == nil || user == nil {
		return false
	}
	if env.EnableBypassAllowlist {
		// Narrowed: the named users and teams decide, and an admin still passes unless
		// BlockAdminOverride is set. That is exactly the allowlist helper's own logic.
		return CanBypassEnvironmentSequence(ctx, env, user, isRepoAdmin)
	}
	if isRepoAdmin && !env.BlockAdminOverride {
		return true
	}
	return canDispatch
}

// ErrApprovalRefused marks a refusal the caller is not permitted to make, so the API answers
// 403 rather than 500. A user the forge does not permit to approve is refused at the
// endpoint, not merely offered no button (F5d, SC 21).
type ErrApprovalRefused struct {
	Err *delivery_model.Error
}

func (e *ErrApprovalRefused) Error() string { return e.Err.Error() }

func (e *ErrApprovalRefused) Unwrap() error { return e.Err }

func refuse(message, action string) error {
	return &ErrApprovalRefused{Err: &delivery_model.Error{Message: message, SuggestedAction: action}}
}

// ApprovalRequest is one approve or reject call, with the caller's authorization already
// resolved by Gitea's own permission check.
type ApprovalRequest struct {
	Approval    *delivery_model.Approval
	Environment *delivery_model.Environment
	Actor       *user_model.User
	// Event is delivery.AuditApproved or delivery.AuditRejected. There is no third verb:
	// the log records what happened, and nothing else releases a held job.
	Event       string
	Reason      string
	IsRepoAdmin bool
	CanDispatch bool
}

// ApprovalDecision is what an approve or reject call leaves behind.
type ApprovalDecision struct {
	Approval          *delivery_model.Approval
	State             string
	ApprovalsCount    int64
	RequiredApprovals int64
}

// Decide records one approval or rejection.
//
// It is the ONLY way to release a held job: the gate reads the audit log this writes, and
// there is no flag anywhere that says "let it through" (F5d). Every decision is an audit
// event naming the approver, their denormalized login and the time (F5c).
func Decide(ctx context.Context, req ApprovalRequest) (*ApprovalDecision, error) {
	if req.Approval == nil || req.Environment == nil || req.Actor == nil {
		return nil, refuse("the approval, its environment or the acting user is missing",
			"Call POST /api/delivery/v1/approvals/{id}/approve with a signed-in token.")
	}
	if req.Event != delivery_model.AuditApproved && req.Event != delivery_model.AuditRejected {
		return nil, refuse(fmt.Sprintf("%q is neither an approval nor a rejection", req.Event),
			"Call the approve or the reject endpoint; those are the only two decisions.")
	}
	if req.Environment.ApprovalPolicy == "" || req.Environment.ApprovalPolicy == delivery_model.PolicyNone {
		return nil, refuse(
			fmt.Sprintf("environment %q has no approval policy, so nothing about this deploy is held", req.Environment.Name),
			"Set the environment's approval_policy to any_approver or others_only if deploys there should be gated.")
	}
	if !CanApproveEnvironment(ctx, req.Environment, req.Actor, req.IsRepoAdmin, req.CanDispatch) {
		return nil, refuse(
			fmt.Sprintf("your account may not approve deploys to %q", req.Environment.Name),
			"Ask for write permission on the Actions unit of this repository, or to be added to the environment's approver allowlist.")
	}
	if req.Environment.ApprovalPolicy == delivery_model.PolicyOthersOnly &&
		req.Event == delivery_model.AuditApproved &&
		req.Actor.ID == req.Approval.RequesterID {
		return nil, refuse(
			fmt.Sprintf("environment %q is set to others_only and you asked for this deploy", req.Environment.Name),
			"Ask someone else with approval rights to approve it, or set the environment's approval_policy to any_approver.")
	}

	votes, err := delivery_model.VotesForApproval(ctx, req.Approval)
	if err != nil {
		return nil, err
	}
	state, _ := delivery_model.ProjectApprovalState(
		req.Environment.ApprovalPolicy, req.Environment.RequiredApprovals, req.Approval.RequesterID, votes)
	if state == delivery_model.ApprovalRejected {
		return nil, refuse(
			"this deploy was already rejected, and a rejection ends the deploy",
			"Dispatch the deploy again from the grid; the rejected run does not proceed later.")
	}
	for _, v := range votes {
		if v.Event == delivery_model.AuditApproved && v.ActorID == req.Actor.ID {
			return nil, refuse(
				"you have already approved this deploy",
				"A second approval has to come from a different user; required_approvals counts distinct approvers.")
		}
	}

	event := &delivery_model.AuditEvent{
		Event:       req.Event,
		ActorID:     req.Actor.ID,
		ActorLogin:  req.Actor.Name,
		RepoID:      req.Approval.RepoID,
		Environment: req.Approval.Environment,
		ReleaseTag:  req.Approval.ReleaseTag,
		SHA:         req.Approval.SHA,
		RunID:       req.Approval.RunID,
		RunURL:      req.Approval.RunURL,
		Reason:      req.Reason,
		Source:      delivery_model.SourceUI,
	}
	if err := delivery_model.AppendAuditEvent(ctx, event); err != nil {
		return nil, err
	}

	state, count, required, err := delivery_model.ResolveApprovalState(ctx, req.Approval)
	if err != nil {
		return nil, err
	}
	return &ApprovalDecision{
		Approval:          req.Approval,
		State:             state,
		ApprovalsCount:    count,
		RequiredApprovals: required,
	}, nil
}

// maxHeldRunsPerRepo bounds how many hold rows the grid projects per repository. The grid is
// a page of releases, not an audit trail; a repository with more holds than this has a
// backlog to work through on the approvals view.
const maxHeldRunsPerRepo = 200

// PendingApprovalRuns reports which of a repository's runs are still waiting on an approval.
//
// It reads every vote for the repository in ONE query and resolves each environment once, so
// a page of the grid costs a fixed number of queries however many holds it covers.
func PendingApprovalRuns(ctx context.Context, repoID int64) (map[int64]bool, error) {
	holds, _, err := delivery_model.FindApprovals(ctx,
		builder.Eq{"repo_id": repoID}, "id DESC", maxHeldRunsPerRepo, 0)
	if err != nil {
		return nil, err
	}
	if len(holds) == 0 {
		return map[int64]bool{}, nil
	}

	cond := builder.Eq{"repo_id": repoID}.
		And(builder.In("event", delivery_model.AuditApproved, delivery_model.AuditRejected))
	rows, err := delivery_model.FindAuditEvents(ctx, cond, "occurred_unix ASC, id ASC", 0)
	if err != nil {
		return nil, err
	}
	type voteKey struct {
		environment string
		runID       int64
	}
	votesOf := map[voteKey][]delivery_model.Vote{}
	for _, r := range rows {
		k := voteKey{environment: r.Environment, runID: r.RunID}
		votesOf[k] = append(votesOf[k], delivery_model.Vote{ActorID: r.ActorID, Event: r.Event})
	}

	environments := map[string]*delivery_model.Environment{}
	held := map[int64]bool{}
	for _, h := range holds {
		env, seen := environments[h.Environment]
		if !seen {
			env, err = delivery_model.GetEnvironment(ctx, repoID, h.Environment)
			if err != nil {
				// A hold naming an environment that has since been removed cannot be
				// resolved; it is not evidence that anything is running.
				environments[h.Environment] = nil
				continue
			}
			environments[h.Environment] = env
		}
		if env == nil {
			continue
		}
		state, _ := delivery_model.ProjectApprovalState(env.ApprovalPolicy, env.RequiredApprovals,
			h.RequesterID, votesOf[voteKey{environment: h.Environment, runID: h.RunID}])
		if state == delivery_model.ApprovalPending {
			held[h.RunID] = true
		}
	}
	return held, nil
}

// applyHeldRuns overwrites a cell that looks queued with `⏸` when the approvals table says
// its run is still waiting on an approval. It is pure, so the second source of the held
// state is testable with no database (J10).
//
// It only ever narrows an in-progress cell. A cell whose last event is a success, a failure
// or a rejection has already reached a terminal state, and a stale hold row must not repaint
// it.
func applyHeldRuns(cells map[string][]Cell, heldRuns map[int64]bool) map[string][]Cell {
	if len(heldRuns) == 0 {
		return cells
	}
	for _, row := range cells {
		for i := range row {
			if row[i].State != CellInProgress || row[i].RunID == 0 || !heldRuns[row[i].RunID] {
				continue
			}
			row[i].State = CellHeld
			row[i].Symbol = cellSymbol(CellHeld, row[i].Successes)
		}
	}
	return cells
}

// ProjectCellsHeld is ProjectCells with the approvals table as `⏸`'s SECOND source (E15).
//
// The first source is the environment's own policy: a requested deploy into a gated
// environment is held rather than queued. That is a projection over the log alone, and it
// cannot tell an approved deploy from one still waiting. The approvals table can, so it wins
// where it speaks.
//
// A failure to read the approvals table degrades to the policy projection and logs, rather
// than failing the whole grid: the grid is a view, and the gate at job assignment — not this
// symbol — is what actually withholds the job.
func ProjectCellsHeld(ctx context.Context, repoID int64, environments, releases []string, events []Event, policies map[string]string) map[string][]Cell {
	cells := ProjectCells(environments, releases, events, policies)
	heldRuns, err := PendingApprovalRuns(ctx, repoID)
	if err != nil {
		log.Error("delivery: read the pending approvals of repo %d: %v — the grid falls back to the environment policy for `⏸`; check the database is reachable", repoID, err)
		return cells
	}
	return applyHeldRuns(cells, heldRuns)
}
