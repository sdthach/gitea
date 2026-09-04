// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"
	"strconv"

	deployments_model "gitea.dev/models/deployments"
	"gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/timeutil"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	deployments_service "gitea.dev/services/deployments"
	"gitea.dev/services/hub/query"

	"xorm.io/builder"
)

// reviewSpec is the reviews resource's whitelist declaration.
//
// A hold is one row per held job and nothing rewrites it, so the set is finite and stable
// and pages by page+limit rather than by cursor. `state` is NOT a filterable field: it
// is projected from the append-only audit log at render time, so there is no column to put
// in a WHERE clause.
var reviewSpec = query.Spec{
	Resource: "reviews",
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

// Review is the reviews resource's response shape: the hold row, plus the state
// projected over the audit log and what the calling user is allowed to do about it.
type Review struct {
	deployments_model.Review
	// State is pending, approved or rejected. It is a projection, never a stored column.
	State             string `json:"state"`
	ReviewPolicy      string `json:"review_policy"`
	ReviewsCount      int64  `json:"reviews_count"`
	RequiredReviewers int64  `json:"required_reviewers"`
	// AgeSeconds is how long the deploy has been held, which is what the pending list
	// sorts a reviewer's attention by.
	AgeSeconds int64 `json:"age_seconds"`
	// CanApprove is the calling user's own authorization, resolved by the same check the
	// endpoint enforces, so a view offers no action it would be refused for.
	CanApprove bool                          `json:"can_approve"`
	Deployment *deployments_model.Deployment `json:"deployment,omitempty"`
}

var reviewIDParam = []hubapi.Param{
	{Name: "id", In: "path", Type: "integer", Description: "Review id.", Required: true},
}

func listReviewsEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "listReviews", Method: http.MethodGet, Path: "/reviews",
			Summary: "List held deploys and their review state",
			Description: "One row per deploy the review gate is holding, each carrying the state projected over the " +
				"append-only audit log: pending, approved or rejected. A row whose state is pending is " +
				"waiting for a reviewer; can_approve says whether the calling user is one of them, resolved by the same " +
				"check the approve endpoint enforces. The environment view and the deployment matrix are clients of this " +
				"endpoint. Finite and stable, so it pages by page+limit.",
			Tag: "reviews", Query: &reviewSpec, Response: "Review", ResponseIs: "array",
		},
		Handler: ListReviews,
	}
}

func approveEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "approve", Method: http.MethodPost, Path: "/reviews/{id}/approve",
			Summary: "Approve a held deploy",
			Description: "Appends a review to the audit log naming the reviewer and the time, which is the only " +
				"thing that releases the held job — there is no flag anywhere that says let it through, and no CLI path " +
				"around the gate. A user the forge does not permit to approve is refused HERE with 403, not " +
				"merely offered no button. Under others_only the requester's own review is refused.",
			Tag: "reviews", PathParams: reviewIDParam, Response: "Review", ResponseIs: "object",
		},
		Handler: ApproveReview,
	}
}

func rejectEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "reject", Method: http.MethodPost, Path: "/reviews/{id}/reject",
			Summary: "Reject a held deploy",
			Description: "Ends the deploy: the run does not proceed later, and the rejection is an audit event naming the " +
				"reviewer and the time. Authorized by the same check as approve.",
			Tag: "reviews", PathParams: reviewIDParam, Response: "Review", ResponseIs: "object",
		},
		Handler: RejectReview,
	}
}

// ListReviews answers GET /reviews.
func ListReviews(ctx *context.APIContext) {
	q, ok := hubapi.ParseQuery(ctx, reviewSpec)
	if !ok {
		return
	}
	repoIDs, ok := accessibleRepoIDs(ctx)
	if !ok {
		return
	}
	if len(repoIDs) == 0 {
		hubapi.RenderPage(ctx, q, 0, []*Review{})
		return
	}

	cond := q.Cond().And(builder.In("repo_id", repoIDs))
	rows, total, err := deployments_model.FindReviews(ctx, cond, q.OrderBy(), q.Limit, q.Offset())
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	out := make([]*Review, 0, len(rows))
	now := int64(timeutil.TimeStampNow())
	// Permission is resolved once per repository rather than once per row: a page of holds
	// otherwise costs one permission lookup per hold.
	canApprove := map[int64]bool{}
	for _, row := range rows {
		state, count, required, err := deployments_model.ResolveReviewState(ctx, row)
		if err != nil {
			// A hold whose environment has since been removed still belongs in the list;
			// it just cannot say what would release it.
			state, count, required = deployments_model.ReviewPending, 0, 0
		}
		allowed, seen := canApprove[row.RepoID]
		if !seen {
			allowed = callerMayApprove(ctx, row.RepoID, row.Environment)
			canApprove[row.RepoID] = allowed
		}
		out = append(out, &Review{
			Review:            *row,
			State:             state,
			ReviewPolicy:      reviewPolicyOf(ctx, row),
			ReviewsCount:      count,
			RequiredReviewers: required,
			AgeSeconds:        now - int64(row.CreatedUnix),
			CanApprove:        allowed && state == deployments_model.ReviewPending,
		})
	}
	if err := expandReviews(ctx, q.Expand, out); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	hubapi.RenderPage(ctx, q, total, out)
}

// reviewPolicyOf reports the live policy of a hold's environment, so a client can explain
// why a deploy is held without a second request.
func reviewPolicyOf(ctx *context.APIContext, a *deployments_model.Review) string {
	env, err := deployments_model.GetEnvironment(ctx, a.RepoID, a.Environment)
	if err != nil {
		return ""
	}
	return env.ReviewPolicy
}

// callerMayApprove answers the same question the approve endpoint enforces, so a view never
// offers an action that would be refused.
func callerMayApprove(ctx *context.APIContext, repoID int64, environment string) bool {
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		return false
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		return false
	}
	env, err := deployments_model.GetEnvironment(ctx, repoID, environment)
	if err != nil {
		return false
	}
	return deployments_service.CanApproveEnvironment(ctx, env, ctx.Doer, perm.IsAdmin(), perm.CanWrite(unit.TypeActions))
}

// expandReviews fills the whitelisted sub-resources, one level deep.
func expandReviews(ctx *context.APIContext, expand []string, rows []*Review) error {
	if len(rows) == 0 || len(expand) == 0 {
		return nil
	}
	for _, name := range expand {
		if name != "deployment" {
			continue
		}
		for _, row := range rows {
			cond := builder.Eq{"repo_id": row.RepoID, "run_id": row.RunID, "environment": row.Environment}
			found, err := deployments_model.FindDeployments(ctx, cond, "id DESC", 1)
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

// ApproveReview answers POST /reviews/{id}/approve.
func ApproveReview(ctx *context.APIContext) { decide(ctx, deployments_model.AuditApproved) }

// RejectReview answers POST /reviews/{id}/reject.
func RejectReview(ctx *context.APIContext) { decide(ctx, deployments_model.AuditRejected) }

// decide is the shared body of approve and reject: they differ only in the event they append.
func decide(ctx *context.APIContext, event string) {
	id, err := strconv.ParseInt(ctx.PathParam("id"), 10, 64)
	if err != nil || id <= 0 {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_review_id",
			"the review id in the path is not a number",
			"Call POST "+BasePath+"/reviews/{id}/approve with an id from "+BasePath+"/reviews.")
		return
	}

	review, err := deployments_model.GetReviewByID(ctx, id)
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusNotFound, err)
		return
	}

	repo, err := repo_model.GetRepositoryByID(ctx, review.RepoID)
	if err != nil {
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			"the repository this review belongs to is not visible to you",
			"Check your token's account can see the repository the held run belongs to.")
		return
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if !perm.CanRead(unit.TypeActions) {
		hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
			"your account has no access to the Actions unit of "+repo.FullName(),
			"Ask a repository administrator for permission on Actions.")
		return
	}

	env, err := deployments_model.GetEnvironment(ctx, review.RepoID, review.Environment)
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusNotFound, err)
		return
	}

	decision, err := deployments_service.Decide(ctx, deployments_service.ReviewRequest{
		Review:      review,
		Environment: env,
		Actor:       ctx.Doer,
		Event:       event,
		Reason:      ctx.FormString("reason"),
		IsRepoAdmin: perm.IsAdmin(),
		CanDispatch: perm.CanWrite(unit.TypeActions),
	})
	if err != nil {
		if refused, ok := err.(*deployments_service.ErrReviewRefused); ok {
			hubapi.RenderHubError(ctx, http.StatusForbidden, refused.Err)
			return
		}
		ctx.APIErrorInternal(err)
		return
	}

	ctx.JSON(http.StatusOK, &Review{
		Review:            *decision.Review,
		State:             decision.State,
		ReviewPolicy:      env.ReviewPolicy,
		ReviewsCount:      decision.ReviewsCount,
		RequiredReviewers: decision.RequiredReviewers,
		AgeSeconds:        int64(timeutil.TimeStampNow()) - int64(decision.Review.CreatedUnix),
		CanApprove:        false,
	})
}
