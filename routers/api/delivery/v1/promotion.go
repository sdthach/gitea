// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/json"
	"gitea.dev/services/context"
	delivery_service "gitea.dev/services/delivery"
)

// maxPromotionBody caps the request body. An override reason is a sentence, not an upload.
const maxPromotionBody = 16 << 10

// promotionBody is POST /deployments. Deploy and rollback send the same shape and differ
// only in release_tag: rolling back is deploying a prior release, not a second path.
type promotionBody struct {
	Repo           string `json:"repo"`
	Environment    string `json:"environment"`
	ReleaseTag     string `json:"release_tag"`
	OverrideReason string `json:"override_reason"`
	Confirm        bool   `json:"confirm"`
}

var promotionBodyParams = []Param{
	{Name: "repo", In: "body", Type: "string", Required: true, Description: "Target repository as owner/name."},
	{Name: "environment", In: "body", Type: "string", Required: true, Description: "Target environment, for example prod."},
	{Name: "release_tag", In: "body", Type: "string", Required: true, Description: "Release tag to deploy. A prior tag is a rollback; it is the same request."},
	{Name: "override_reason", In: "body", Type: "string", Description: "Why the environment's sequence rule is being bypassed. Required when the deploy plan reports outcome \"override\"; it is written to the audit log."},
	{Name: "confirm", In: "body", Type: "boolean", Description: "False, the default, returns the deploy plan and dispatches nothing — the first of the two confirm steps. True dispatches."},
}

func createDeploymentEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "createDeployment", Method: http.MethodPost, Path: "/deployments",
			Summary: "Plan or dispatch a deploy of a release to an environment",
			Description: "Two steps. With confirm false — the default — nothing is dispatched and the response names the " +
				"target environment, the release tag, the release currently live there, and what the environment's sequence " +
				"rule decided. With confirm true the environment's deploy workflow is dispatched at the release tag, as the " +
				"calling user, so Gitea names the human who asked. " +
				"Rolling back is this same call with a prior release tag; there is no separate rollback path. " +
				"An environment that sets require_predecessor refuses a release its predecessor has never held, unless the " +
				"caller can bypass — the same helper and the same allowlist fields branch protection uses — in which case the " +
				"override and its reason are appended to the audit log. " +
				"Authorized by Gitea's own write check on the Actions unit.",
			Tag: "deployments", Body: promotionBodyParams,
			// deploy and rollback are one operation: the request they compose is identical
			// but for release_tag, so publishing two endpoints would publish two ways to
			// reach one rule.
			CLINames: []string{"deploy", "rollback"},
			Response: "Promotion", ResponseIs: "object",
		},
		Handler: CreateDeployment,
	}
}

// CreateDeployment answers POST /deployments. It is the only write path the API exposes onto
// the deployment tables, and it reaches the dispatch through services/delivery.Promote, the
// same call the page and the CLI make — there is no path around the sequence rule.
func CreateDeployment(ctx *context.APIContext) {
	body, ok := readPromotionBody(ctx)
	if !ok {
		return
	}

	owner, name, found := strings.Cut(strings.TrimSpace(body.Repo), "/")
	if !found || owner == "" || name == "" {
		apiError(ctx, http.StatusBadRequest, "bad_repo",
			fmt.Sprintf("repo must be owner/name, got %q", body.Repo),
			"Send repo as owner/name, for example \"gitea/gitea\". List "+BasePath+"/repos to see what you can deploy.")
		return
	}

	repo, err := repo_model.GetRepositoryByOwnerAndName(ctx, owner, name)
	if err != nil {
		apiError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository "+owner+"/"+name+" is visible to you",
			"Check the owner and repository name, and that your token's account can see the repository.")
		return
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	// The same check the rest of Gitea makes for dispatching a workflow, applied in process.
	// A CLI deploy is refused exactly where the grid refuses.
	if !perm.CanWrite(unit.TypeActions) {
		apiError(ctx, http.StatusForbidden, "forbidden",
			"your account has no write access to the Actions unit of "+repo.FullName(),
			"Ask a repository administrator for write permission on Actions, or enable the Actions unit on the repository.")
		return
	}

	result, err := delivery_service.Promote(ctx, delivery_service.PromotionRequest{
		Repo:           repo,
		Doer:           ctx.Doer,
		IsRepoAdmin:    perm.IsAdmin(),
		Environment:    body.Environment,
		ReleaseTag:     body.ReleaseTag,
		OverrideReason: body.OverrideReason,
		Confirm:        body.Confirm,
	})
	if err != nil {
		status := http.StatusBadRequest
		if notFound, ok := errors.AsType[*delivery_service.ErrPromotionNotFound](err); ok {
			status, err = http.StatusNotFound, notFound.Err
		}
		renderHubError(ctx, status, err)
		return
	}

	switch {
	case result.Outcome == delivery_service.OutcomeRefuse:
		// A refusal is the rule speaking, not a malformed request, so it renders as the
		// permission answer it is — with the action that would make it succeed.
		ctx.JSON(http.StatusForbidden, result)
	case body.Confirm && result.RequiresOverrideReason && !result.Confirmed:
		ctx.JSON(http.StatusBadRequest, result)
	case result.Confirmed:
		ctx.JSON(http.StatusCreated, result)
	default:
		ctx.JSON(http.StatusOK, result)
	}
}

// readPromotionBody decodes the body, rendering its own rejection.
func readPromotionBody(ctx *context.APIContext) (*promotionBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(ctx.Req.Body, maxPromotionBody+1))
	if err != nil {
		apiError(ctx, http.StatusBadRequest, "unreadable_body",
			"the request body could not be read", "Retry the request; the body was cut short.")
		return nil, false
	}
	if len(raw) > maxPromotionBody {
		apiError(ctx, http.StatusBadRequest, "body_too_large",
			"the request body is larger than 16KiB",
			"Shorten override_reason; it is a sentence saying why the sequence rule is being bypassed.")
		return nil, false
	}
	body := new(promotionBody)
	if err := json.Unmarshal(raw, body); err != nil {
		apiError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"repo": "owner/name", "environment": "prod", "release_tag": "v1.0", "confirm": true}.`)
		return nil, false
	}
	return body, true
}
