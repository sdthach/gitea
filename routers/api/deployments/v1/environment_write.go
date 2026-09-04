// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"fmt"
	"io"
	"net/http"
	"strconv"

	deployments_model "gitea.dev/models/deployments"
	"gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/modules/json"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
)

const maxEnvironmentBody = 16 << 10

type environmentBody struct {
	RepoID                 int64    `json:"repo_id"`
	Name                   string   `json:"name"`
	SortOrder              int64    `json:"sort_order"`
	ReviewPolicy           string   `json:"review_policy"`
	RequiredReviewers      int64    `json:"required_reviewers"`
	DependsOn              []string `json:"depends_on"`
	RequirePriorDeployment bool     `json:"require_prior_deployment"`
	ReleasesOnly           bool     `json:"releases_only"`
	AdminsCanBypass        *bool    `json:"admins_can_bypass"`
	RestrictReviewers      bool     `json:"restrict_reviewers"`
	ReviewerUserIDs        []int64  `json:"reviewer_user_ids"`
	ReviewerTeamIDs        []int64  `json:"reviewer_team_ids"`

	AutoPromote            bool     `json:"auto_promote"`
	WaitMinutes            int      `json:"wait_minutes"`
	RequiredStatusContexts []string `json:"required_status_contexts"`
	ExclusiveLock          bool     `json:"exclusive_lock"`
	// DeployWindow takes the nested object directly; the CLI's ObjectBody flag kind marshals
	// it from a JSON-string flag value. DeployWindowDaysMask and its three siblings remain as
	// an alternative flat form for a caller that would rather send four scalars than one
	// object — either form writes the same column. Sending neither leaves the stored window
	// exactly as it was; sending deploy_window: null, or a days_mask of 0 in either form,
	// clears it.
	DeployWindow           *deployments_model.DeployWindow `json:"deploy_window"`
	DeployWindowDaysMask   int                             `json:"deploy_window_days_mask"`
	DeployWindowFromMinute int                             `json:"deploy_window_from_minute"`
	DeployWindowToMinute   int                             `json:"deploy_window_to_minute"`
	DeployWindowTimezone   string                          `json:"deploy_window_timezone"`

	// hasDeployWindowKey and hasFlatWindowKeys record whether the raw request body carried
	// the nested key or any of the four flat keys at all — set by readEnvironmentBody, which
	// alone sees the raw JSON. A struct field left at its zero value by json.Unmarshal is
	// indistinguishable from one the caller never mentioned; these two flags are what let
	// resolveDeployWindow tell "omitted" apart from "explicitly zero".
	hasDeployWindowKey bool
	hasFlatWindowKeys  bool
}

var environmentBodyParams = []hubapi.Param{
	{Name: "repo_id", In: "body", Type: "integer", Description: "Repository id; 0 is the instance-wide default set."},
	{Name: "name", In: "body", Type: "string", Required: true, Description: "Environment name."},
	{Name: "sort_order", In: "body", Type: "integer", Description: "Render order."},
	{Name: "review_policy", In: "body", Type: "string", Description: "Review policy. Defaults to none.", Enum: deployments_model.ReviewPolicies},
	{Name: "required_reviewers", In: "body", Type: "integer", Description: "Reviews needed. Defaults to 1."},
	{Name: "depends_on", In: "body", Type: "array", Description: "Environments a release must pass through first."},
	{Name: "require_prior_deployment", In: "body", Type: "boolean", Description: "Gate when a dependency hasn't held the release."},
	{Name: "releases_only", In: "body", Type: "boolean", Description: "Refuse prereleases; this environment takes finished releases only."},
	{Name: "admins_can_bypass", In: "body", Type: "boolean", Description: "Let a repo admin bypass the gate. Defaults to true on create; an update that omits it leaves the existing value unchanged."},
	{Name: "restrict_reviewers", In: "body", Type: "boolean", Description: "Enable bypass allowlist."},
	{Name: "reviewer_user_ids", In: "body", Type: "array", Description: "User IDs allowed to bypass."},
	{Name: "reviewer_team_ids", In: "body", Type: "array", Description: "Team IDs allowed to bypass."},
	{Name: "auto_promote", In: "body", Type: "boolean", Description: "Deploy the same release here automatically once every environment in depends_on holds it live."},
	{Name: "wait_minutes", In: "body", Type: "integer", Description: "Hold every deploy this many minutes after it is requested before dispatching. 0..10080 (a week)."},
	{Name: "required_status_contexts", In: "body", Type: "array", Description: "Commit status contexts that must report success on the release commit before dispatching. Up to 20 entries."},
	{Name: "exclusive_lock", In: "body", Type: "boolean", Description: "Refuse a deploy while another is already waiting or running on this environment."},
	{Name: "deploy_window", In: "body", Type: "object", Description: "The deploy window as one object: {days_mask, from_minute, to_minute, timezone}. An alternative to the four deploy_window_* scalars below; sending neither leaves the stored window unchanged, and null or a zero days_mask in either form clears it."},
	{Name: "deploy_window_days_mask", In: "body", Type: "integer", Description: "Days a deploy may dispatch, as a bitmask: bit 0 Sunday .. bit 6 Saturday, 1..127. Omit both this and deploy_window to leave the window unchanged; 0 clears it."},
	{Name: "deploy_window_from_minute", In: "body", Type: "integer", Description: "Window open time, minutes since local midnight in deploy_window_timezone. After deploy_window_to_minute wraps the window past midnight."},
	{Name: "deploy_window_to_minute", In: "body", Type: "integer", Description: "Window close time, minutes since local midnight in deploy_window_timezone."},
	{Name: "deploy_window_timezone", In: "body", Type: "string", Description: "IANA timezone the window is evaluated in, for example \"America/New_York\"."},
}

var environmentIDParam = []hubapi.Param{
	{Name: "id", In: "path", Type: "integer", Description: "Environment id.", Required: true},
}

func createEnvironmentEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "createEnvironment", Method: http.MethodPost, Path: "/environments",
			Summary: "Create an environment",
			Description: "Site admin for instance-wide defaults (repo_id 0), repo admin for " +
				"repository environments. Validates name, review policy and dependencies.",
			Tag: "environments", Body: environmentBodyParams,
			CLINames: []string{"environment-create"},
			Response: "Environment", ResponseIs: "object",
		},
		Handler: CreateEnvironmentHandler,
	}
}

func updateEnvironmentEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "updateEnvironment", Method: http.MethodPut, Path: "/environments/{id}",
			Summary: "Update an environment",
			Description: "Replaces the environment's configuration. Site admin for instance-wide " +
				"defaults, repo admin for repository environments.",
			Tag: "environments", PathParams: environmentIDParam, Body: environmentBodyParams,
			CLINames: []string{"environment-update"},
			Response: "Environment", ResponseIs: "object",
		},
		Handler: UpdateEnvironmentHandler,
	}
}

func deleteEnvironmentEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "deleteEnvironment", Method: http.MethodDelete, Path: "/environments/{id}",
			Summary:     "Delete an environment",
			Description: "Same authorization as create and update.",
			Tag:         "environments", PathParams: environmentIDParam,
			CLINames: []string{"environment-delete"},
			Response: "Environment", ResponseIs: "object",
		},
		Handler: DeleteEnvironmentHandler,
	}
}

// canWriteEnvironment is the one write rule; the gate and each row's can_write both read it.
func canWriteEnvironment(ctx *context.APIContext, repoID int64) (bool, error) {
	if repoID == deployments_model.DefaultsRepoID {
		return ctx.Doer.IsAdmin, nil
	}
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		return false, err
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		return false, err
	}
	return perm.IsAdmin(), nil
}

// requireEnvironmentAdmin renders the refusal canWriteEnvironment implies.
func requireEnvironmentAdmin(ctx *context.APIContext, repoID int64) bool {
	allowed, err := canWriteEnvironment(ctx, repoID)
	switch {
	case repo_model.IsErrRepoNotExist(err):
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			fmt.Sprintf("no repository with id %d is visible to you", repoID),
			"Check the repo_id and that your token's account can see the repository.")
		return false
	case err != nil:
		ctx.APIErrorInternal(err)
		return false
	case allowed:
		return true
	case repoID == deployments_model.DefaultsRepoID:
		hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
			"only site administrators may manage instance-wide default environments",
			"Ask a Gitea administrator, or create a repository-specific environment instead.")
		return false
	}
	hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
		"only repository administrators may manage environments of "+environmentRepoName(ctx, repoID),
		"Ask a repository administrator for admin access.")
	return false
}

func environmentRepoName(ctx *context.APIContext, repoID int64) string {
	if repo, err := repo_model.GetRepositoryByID(ctx, repoID); err == nil {
		return repo.FullName()
	}
	return fmt.Sprintf("repository %d", repoID)
}

// CreateEnvironmentHandler answers POST /environments.
func CreateEnvironmentHandler(ctx *context.APIContext) {
	body, ok := readEnvironmentBody(ctx)
	if !ok {
		return
	}
	if !requireEnvironmentAdmin(ctx, body.RepoID) {
		return
	}
	env := bodyToEnvironment(body)
	env.DeployWindow = resolveDeployWindow(body, nil)
	env.AdminsCanBypass = true // GitHub's own default; overridden below when the body sets it
	if body.AdminsCanBypass != nil {
		env.AdminsCanBypass = *body.AdminsCanBypass
	}
	if err := deployments_model.CreateEnvironment(ctx, env); err != nil {
		hubapi.RenderHubError(ctx, http.StatusBadRequest, err)
		return
	}
	renderEnvironment(ctx, http.StatusCreated, env)
}

// UpdateEnvironmentHandler answers PUT /environments/{id}.
func UpdateEnvironmentHandler(ctx *context.APIContext) {
	id, err := strconv.ParseInt(ctx.PathParam("id"), 10, 64)
	if err != nil || id <= 0 {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_id", "the id in the path is not a number",
			"Call PUT "+BasePath+"/environments/{id} with an id from "+BasePath+"/environments.")
		return
	}
	existing, err := deployments_model.GetEnvironmentByID(ctx, id)
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusNotFound, err)
		return
	}
	if !requireEnvironmentAdmin(ctx, existing.RepoID) {
		return
	}
	body, ok := readEnvironmentBody(ctx)
	if !ok {
		return
	}
	env := bodyToEnvironment(body)
	env.ID = id
	env.RepoID = existing.RepoID
	env.DeployWindow = resolveDeployWindow(body, existing.DeployWindow)
	env.AdminsCanBypass = existing.AdminsCanBypass // unchanged unless the body sets it
	if body.AdminsCanBypass != nil {
		env.AdminsCanBypass = *body.AdminsCanBypass
	}
	if err := deployments_model.UpdateEnvironment(ctx, env); err != nil {
		hubapi.RenderHubError(ctx, http.StatusBadRequest, err)
		return
	}
	renderEnvironment(ctx, http.StatusOK, env)
}

// DeleteEnvironmentHandler answers DELETE /environments/{id}.
func DeleteEnvironmentHandler(ctx *context.APIContext) {
	id, err := strconv.ParseInt(ctx.PathParam("id"), 10, 64)
	if err != nil || id <= 0 {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_id", "the id in the path is not a number",
			"Call DELETE "+BasePath+"/environments/{id} with an id from "+BasePath+"/environments.")
		return
	}
	existing, err := deployments_model.GetEnvironmentByID(ctx, id)
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusNotFound, err)
		return
	}
	if !requireEnvironmentAdmin(ctx, existing.RepoID) {
		return
	}
	if err := deployments_model.DeleteEnvironment(ctx, id); err != nil {
		hubapi.RenderHubError(ctx, http.StatusNotFound, err)
		return
	}
	ctx.JSON(http.StatusNoContent, nil)
}

// bodyToEnvironment carries every field with a plain zero-value default. AdminsCanBypass is
// not among them — a pointer, so its caller can tell an absent field from an explicit false —
// and is set by CreateEnvironmentHandler and UpdateEnvironmentHandler once they know whether
// they are defaulting it or preserving the existing row's value.
func bodyToEnvironment(body *environmentBody) *deployments_model.Environment {
	env := &deployments_model.Environment{
		RepoID:                 body.RepoID,
		Name:                   body.Name,
		SortOrder:              body.SortOrder,
		ReviewPolicy:           body.ReviewPolicy,
		RequiredReviewers:      body.RequiredReviewers,
		DependsOn:              body.DependsOn,
		RequirePriorDeployment: body.RequirePriorDeployment,
		ReleasesOnly:           body.ReleasesOnly,
		RestrictReviewers:      body.RestrictReviewers,
		ReviewerUserIDs:        body.ReviewerUserIDs,
		ReviewerTeamIDs:        body.ReviewerTeamIDs,
		AutoPromote:            body.AutoPromote,
		WaitMinutes:            body.WaitMinutes,
		RequiredStatusContexts: body.RequiredStatusContexts,
		ExclusiveLock:          body.ExclusiveLock,
	}
	return env
}

// resolveDeployWindow decides the window a create or update actually stores. existing is the
// row's current window (nil on create, since there is nothing yet to preserve).
//
//   - The nested deploy_window key, when the body carries it at all: null, or an object with
//     days_mask 0, clears the window; anything else stores that object verbatim.
//   - Failing that, any of the four flat deploy_window_* keys: a days_mask of 0 clears the
//     window (the same rule an omitted flat form used to apply to every write, which is the
//     bug this function fixes); otherwise the four scalars are assembled into one window.
//   - Neither form present: the window is left exactly as existing, so a write touching some
//     other field never silently clears a window nobody asked to change.
func resolveDeployWindow(body *environmentBody, existing *deployments_model.DeployWindow) *deployments_model.DeployWindow {
	switch {
	case body.hasDeployWindowKey:
		if body.DeployWindow == nil || body.DeployWindow.DaysMask == 0 {
			return nil
		}
		return body.DeployWindow
	case body.hasFlatWindowKeys:
		if body.DeployWindowDaysMask == 0 {
			return nil
		}
		return &deployments_model.DeployWindow{
			DaysMask: body.DeployWindowDaysMask, FromMinute: body.DeployWindowFromMinute,
			ToMinute: body.DeployWindowToMinute, Timezone: body.DeployWindowTimezone,
		}
	default:
		return existing
	}
}

func readEnvironmentBody(ctx *context.APIContext) (*environmentBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(ctx.Req.Body, maxEnvironmentBody+1))
	if err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "unreadable_body",
			"the request body could not be read", "Retry the request.")
		return nil, false
	}
	if len(raw) > maxEnvironmentBody {
		hubapi.APIError(ctx, http.StatusBadRequest, "body_too_large",
			"the request body is larger than 16KiB",
			"An environment definition is small; check whether the request is sending the right content.")
		return nil, false
	}
	body := new(environmentBody)
	if err := json.Unmarshal(raw, body); err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"name": "staging", "review_policy": "none"}.`)
		return nil, false
	}

	// A second, untyped pass over the same bytes is what tells "the caller sent this key"
	// apart from "json.Unmarshal left the field at its zero value" — the distinction
	// resolveDeployWindow needs to leave an omitted window unchanged rather than clearing it.
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err == nil {
		_, body.hasDeployWindowKey = keys["deploy_window"]
		for _, k := range []string{
			"deploy_window_days_mask", "deploy_window_from_minute", "deploy_window_to_minute", "deploy_window_timezone",
		} {
			if _, ok := keys[k]; ok {
				body.hasFlatWindowKeys = true
				break
			}
		}
	}
	return body, true
}
