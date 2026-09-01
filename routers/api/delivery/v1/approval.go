// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"
	"strconv"

	"gitea.dev/models/delivery"
	"gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/timeutil"
	"gitea.dev/services/context"
	delivery_service "gitea.dev/services/delivery"
	"gitea.dev/services/delivery/query"

	"xorm.io/builder"
)

// approvalSpec is the approvals resource's whitelist declaration.
//
// A hold is one row per held job and nothing rewrites it, so the set is finite and stable
// and pages by page+limit rather than by cursor (I7). `state` is NOT a filterable field: it
// is projected from the append-only audit log at render time, so there is no column to put
// in a WHERE clause (E15).
var approvalSpec = query.Spec{
	Resource: "approvals",
	Fields: []query.Field{
		{Name: "id", Column: "id", Kind: query.KindInt},
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt},
		{Name: "environment", Column: "environment", Kind: query.KindString},
		{Name: "run_id", Column: "run_id", Kind: query.KindInt},
		{Name: "job_id", Column: "job_id", Kind: query.KindInt},
		{Name: "release_tag", Column: "release_tag", Kind: query.KindString},
		{Name: "requester_id", Column: "requester_id", Kind: query.KindInt},
		{Name: "requester_login", Column: "requester_login", Kind: query.KindString},
		{Name: "created_unix", Column: "created_unix", Kind: query.KindTime},
	},
	SortFields:   []string{"id", "created_unix", "environment", "release_tag"},
	DefaultSort:  "created_unix",
	DefaultOrder: query.OrderDesc,
	PrimaryKey:   "id",
	SearchFields: []string{"release_tag", "environment", "requester_login"},
	Expands:      []string{"deployment"},
	Paging:       query.PagingOffset,
}

// Approval is the approvals resource's response shape: the hold row, plus the state
// projected over the audit log and what the calling user is allowed to do about it.
type Approval struct {
	delivery.Approval
	// State is pending, approved or rejected. It is a projection, never a stored column.
	State             string `json:"state"`
	ApprovalPolicy    string `json:"approval_policy"`
	ApprovalsCount    int64  `json:"approvals_count"`
	RequiredApprovals int64  `json:"required_approvals"`
	// AgeSeconds is how long the deploy has been held, which is what the pending list
	// sorts a reviewer's attention by (E16).
	AgeSeconds int64 `json:"age_seconds"`
	// CanApprove is the calling user's own authorization, resolved by the same check the
	// endpoint enforces, so a view offers no action it would be refused for (SC 21).
	CanApprove bool                 `json:"can_approve"`
	Deployment *delivery.Deployment `json:"deployment,omitempty"`
}

var approvalIDParam = []Param{
	{Name: "id", In: "path", Type: "integer", Description: "Approval id.", Required: true},
}

func listApprovalsEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "listApprovals", Method: http.MethodGet, Path: "/approvals",
			Summary: "List held deploys and their approval state",
			Description: "One row per deploy the approval gate is holding, each carrying the state projected over the " +
				"append-only audit log: pending, approved or rejected (F5, E15, E16). A row whose state is pending is " +
				"waiting for an approver; can_approve says whether the calling user is one of them, resolved by the same " +
				"check the approve endpoint enforces (SC 21). The environment view and the grid are clients of this " +
				"endpoint (E18, I14). Finite and stable, so it pages by page+limit (I7).",
			Tag: "approvals", Query: &approvalSpec, Response: "Approval", ResponseIs: "array",
		},
		Handler: ListApprovals,
	}
}

func approveEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "approve", Method: http.MethodPost, Path: "/approvals/{id}/approve",
			Summary: "Approve a held deploy",
			Description: "Appends an approval to the audit log naming the approver and the time (F5c), which is the only " +
				"thing that releases the held job — there is no flag anywhere that says let it through, and no CLI path " +
				"around the gate (F5d, K6). A user the forge does not permit to approve is refused HERE with 403, not " +
				"merely offered no button (SC 21). Under others_only the requester's own approval is refused.",
			Tag: "approvals", PathParams: approvalIDParam, Response: "Approval", ResponseIs: "object",
		},
		Handler: ApproveApproval,
	}
}

func rejectEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "reject", Method: http.MethodPost, Path: "/approvals/{id}/reject",
			Summary: "Reject a held deploy",
			Description: "Ends the deploy: the run does not proceed later, and the rejection is an audit event naming the " +
				"approver and the time (F5c, SC 20). Authorized by the same check as approve (SC 21).",
			Tag: "approvals", PathParams: approvalIDParam, Response: "Approval", ResponseIs: "object",
		},
		Handler: RejectApproval,
	}
}

// ListApprovals answers GET /approvals.
func ListApprovals(ctx *context.APIContext) {
	q, ok := parseQuery(ctx, approvalSpec)
	if !ok {
		return
	}
	repoIDs, ok := accessibleRepoIDs(ctx)
	if !ok {
		return
	}
	if len(repoIDs) == 0 {
		renderPage(ctx, q, 0, []*Approval{})
		return
	}

	cond := q.Cond().And(builder.In("repo_id", repoIDs))
	rows, total, err := delivery.FindApprovals(ctx, cond, q.OrderBy(), q.Limit, q.Offset())
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	out := make([]*Approval, 0, len(rows))
	now := int64(timeutil.TimeStampNow())
	// Permission is resolved once per repository rather than once per row: a page of holds
	// otherwise costs one permission lookup per hold.
	canApprove := map[int64]bool{}
	for _, row := range rows {
		state, count, required, err := delivery.ResolveApprovalState(ctx, row)
		if err != nil {
			// A hold whose environment has since been removed still belongs in the list;
			// it just cannot say what would release it.
			state, count, required = delivery.ApprovalPending, 0, 0
		}
		allowed, seen := canApprove[row.RepoID]
		if !seen {
			allowed = callerMayApprove(ctx, row.RepoID, row.Environment)
			canApprove[row.RepoID] = allowed
		}
		out = append(out, &Approval{
			Approval:          *row,
			State:             state,
			ApprovalPolicy:    approvalPolicyOf(ctx, row),
			ApprovalsCount:    count,
			RequiredApprovals: required,
			AgeSeconds:        now - int64(row.CreatedUnix),
			CanApprove:        allowed && state == delivery.ApprovalPending,
		})
	}
	if err := expandApprovals(ctx, q.Expand, out); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	renderPage(ctx, q, total, out)
}

// approvalPolicyOf reports the live policy of a hold's environment, so a client can explain
// why a deploy is held without a second request.
func approvalPolicyOf(ctx *context.APIContext, a *delivery.Approval) string {
	env, err := delivery.GetEnvironment(ctx, a.RepoID, a.Environment)
	if err != nil {
		return ""
	}
	return env.ApprovalPolicy
}

// callerMayApprove answers the same question the approve endpoint enforces, so a view never
// offers an action that would be refused (SC 21).
func callerMayApprove(ctx *context.APIContext, repoID int64, environment string) bool {
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		return false
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		return false
	}
	env, err := delivery.GetEnvironment(ctx, repoID, environment)
	if err != nil {
		return false
	}
	return delivery_service.CanApproveEnvironment(ctx, env, ctx.Doer, perm.IsAdmin(), perm.CanWrite(unit.TypeActions))
}

// expandApprovals fills the whitelisted sub-resources, one level deep (I9).
func expandApprovals(ctx *context.APIContext, expand []string, rows []*Approval) error {
	if len(rows) == 0 || len(expand) == 0 {
		return nil
	}
	for _, name := range expand {
		if name != "deployment" {
			continue
		}
		for _, row := range rows {
			cond := builder.Eq{"repo_id": row.RepoID, "run_id": row.RunID, "environment": row.Environment}
			found, err := delivery.FindDeployments(ctx, cond, "id DESC", 1)
			if err != nil {
				return err
			}
			if len(found) > 0 {
				row.Deployment = found[0]
			}
		}
	}
	return nil
}

// ApproveApproval answers POST /approvals/{id}/approve.
func ApproveApproval(ctx *context.APIContext) { decide(ctx, delivery.AuditApproved) }

// RejectApproval answers POST /approvals/{id}/reject.
func RejectApproval(ctx *context.APIContext) { decide(ctx, delivery.AuditRejected) }

// decide is the shared body of approve and reject: they differ only in the event they append.
func decide(ctx *context.APIContext, event string) {
	id, err := strconv.ParseInt(ctx.PathParam("id"), 10, 64)
	if err != nil || id <= 0 {
		apiError(ctx, http.StatusBadRequest, "bad_approval_id",
			"the approval id in the path is not a number",
			"Call POST "+BasePath+"/approvals/{id}/approve with an id from "+BasePath+"/approvals.")
		return
	}

	approval, err := delivery.GetApprovalByID(ctx, id)
	if err != nil {
		renderHubError(ctx, http.StatusNotFound, err)
		return
	}

	repo, err := repo_model.GetRepositoryByID(ctx, approval.RepoID)
	if err != nil {
		apiError(ctx, http.StatusNotFound, "repo_not_found",
			"the repository this approval belongs to is not visible to you",
			"Check your token's account can see the repository the held run belongs to.")
		return
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if !perm.CanRead(unit.TypeActions) {
		apiError(ctx, http.StatusForbidden, "forbidden",
			"your account has no access to the Actions unit of "+repo.FullName(),
			"Ask a repository administrator for permission on Actions.")
		return
	}

	env, err := delivery.GetEnvironment(ctx, approval.RepoID, approval.Environment)
	if err != nil {
		renderHubError(ctx, http.StatusNotFound, err)
		return
	}

	decision, err := delivery_service.Decide(ctx, delivery_service.ApprovalRequest{
		Approval:    approval,
		Environment: env,
		Actor:       ctx.Doer,
		Event:       event,
		Reason:      ctx.FormString("reason"),
		IsRepoAdmin: perm.IsAdmin(),
		CanDispatch: perm.CanWrite(unit.TypeActions),
	})
	if err != nil {
		if refused, ok := err.(*delivery_service.ErrApprovalRefused); ok {
			renderHubError(ctx, http.StatusForbidden, refused.Err)
			return
		}
		ctx.APIErrorInternal(err)
		return
	}

	ctx.JSON(http.StatusOK, &Approval{
		Approval:          *decision.Approval,
		State:             decision.State,
		ApprovalPolicy:    env.ApprovalPolicy,
		ApprovalsCount:    decision.ApprovalsCount,
		RequiredApprovals: decision.RequiredApprovals,
		AgeSeconds:        int64(timeutil.TimeStampNow()) - int64(decision.Approval.CreatedUnix),
		CanApprove:        false,
	})
}
