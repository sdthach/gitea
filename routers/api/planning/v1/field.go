// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	stdcontext "context"
	"io"
	"net/http"
	"strings"

	issues_model "gitea.dev/models/issues"
	"gitea.dev/models/perm/access"
	planning_model "gitea.dev/models/planning"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/json"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	"gitea.dev/services/hub/query"
	planning_service "gitea.dev/services/planning"
)

// FieldRow is one field as the CRUD endpoints publish it: the row plus the scope it lives in,
// so a client can show where an edit would apply.
type FieldRow struct {
	ID       int64    `json:"id"`
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Kind     string   `json:"kind"`
	Options  []string `json:"options,omitempty"`
	Required bool     `json:"required"`
	Sort     int      `json:"sort"`
	Scope    string   `json:"scope"`
	ScopeID  int64    `json:"scope_id"`
}

func fieldRowFrom(row *planning_model.Field) FieldRow {
	scope, scopeID := planning_service.ScopeInstance, int64(0)
	switch {
	case row.RepoID > 0:
		scope, scopeID = planning_service.ScopeRepo, row.RepoID
	case row.OrgID > 0:
		scope, scopeID = planning_service.ScopeOrg, row.OrgID
	}
	return FieldRow{
		ID: row.ID, Key: row.Key, Label: row.Label, Kind: row.Kind, Options: row.Options,
		Required: row.Required, Sort: row.Sort, Scope: scope, ScopeID: scopeID,
	}
}

// FieldDeleteResult is DELETE /fields/{id}'s own response: how many recorded values were
// cascaded away with the field.
type FieldDeleteResult struct {
	DeletedValues int64 `json:"deleted_values"`
}

// IssueFields is GET /issues/{issue_id}/fields' response: the fields visible from the issue's
// repository, and this issue's own values among them, keyed by field key.
type IssueFields struct {
	Fields []planning_service.VisibleField `json:"fields"`
	Values map[string]any                  `json:"values"`
}

var fieldSpec = query.Spec{
	Resource: "fields",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "org_id", Column: "org_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
	},
	PrimaryKey: "id",
	Paging:     query.PagingOffset,
}

var fieldIDParam = []hubapi.Param{
	{Name: "id", In: "path", Type: "integer", Required: true, Description: "The field's id."},
}

var fieldBodyParams = []hubapi.Param{
	{Name: "repo_id", In: "body", Type: "integer", Description: "Scope the field to this repository. Mutually exclusive with org_id; both zero is the instance scope."},
	{Name: "org_id", In: "body", Type: "integer", Description: "Scope the field to this organization. Mutually exclusive with repo_id."},
	{Name: "key", In: "body", Type: "string", Required: true, Description: "A lower-case slug: a letter, then up to 39 letters, digits or underscores."},
	{Name: "label", In: "body", Type: "string", Required: true, Description: "1-100 characters."},
	{Name: "kind", In: "body", Type: "string", Required: true, Enum: planning_service.FieldKinds, Description: "One of int, text, date or select. Fixed once the field is created."},
	{Name: "options", In: "body", Type: "array", Description: "select only: 1-50 distinct options, each at most 100 characters."},
	{Name: "required", In: "body", Type: "boolean", Description: "Whether an issue's value may be cleared once set."},
	{Name: "sort", In: "body", Type: "integer", Description: "Tie-breaker for display order among a scope's own fields."},
}

func getFieldsEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getFields", Method: http.MethodGet, Path: "/fields",
			Summary: "The custom fields visible from a repository or an organization",
			Description: "repo_id reads a repository's own fields, its organization's when it has one, and the " +
				"instance's, nearest scope shadowing by key. org_id reads an organization's own fields and the " +
				"instance's. Neither reads the instance scope alone. Readable by anyone who can read the named " +
				"repository's Issues unit, or to whom the named organization is visible.",
			Tag: "fields", Query: &fieldSpec, Response: "Field", ResponseIs: "array",
			CLINames: []string{"fields"},
		},
		Handler: GetFields,
	}
}

func createFieldEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "createField", Method: http.MethodPost, Path: "/fields",
			Summary: "Create a custom field",
			Description: "Scope admin: a repository administrator for repo_id, an organization owner for org_id, " +
				"a site administrator for the instance scope (neither given). Refused bad_scope, bad_key, bad_label, " +
				"bad_kind, options_required or field_exists.",
			Tag: "fields", Body: fieldBodyParams,
			CLINames: []string{"field-create"},
			Response: "Field", ResponseIs: "object",
		},
		Handler: CreateField,
	}
}

func updateFieldEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "updateField", Method: http.MethodPut, Path: "/fields/{id}",
			Summary: "Update a custom field",
			Description: "Same scope admin check as creating one. Scope and kind are both fixed at creation: " +
				"changing kind is refused kind_immutable, since values may already exist under it.",
			Tag: "fields", PathParams: fieldIDParam, Body: fieldBodyParams,
			CLINames: []string{"field-update"},
			Response: "Field", ResponseIs: "object",
		},
		Handler: UpdateField,
	}
}

func deleteFieldEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "deleteField", Method: http.MethodDelete, Path: "/fields/{id}",
			Summary:     "Delete a custom field",
			Description: "Cascades every recorded value naming the field; the reply says how many were removed.",
			Tag:         "fields", PathParams: fieldIDParam,
			CLINames: []string{"field-delete"},
			Response: "FieldDeleteResult", ResponseIs: "object",
		},
		Handler: DeleteField,
	}
}

func getIssueFieldsEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getIssueFields", Method: http.MethodGet, Path: "/issues/{issue_id}/fields",
			Summary:     "An issue's visible fields and its own recorded values",
			Description: "Scoped by Gitea's own permission check on the Issues or Pull Requests unit, exactly as GET /issues/{issue_id}.",
			Tag:         "fields", PathParams: issueParam,
			CLINames: []string{"issue-fields"},
			Response: "IssueFields", ResponseIs: "object",
		},
		Handler: GetIssueFields,
	}
}

func setIssueFieldsEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "setIssueFields", Method: http.MethodPut, Path: "/issues/{issue_id}/fields",
			Summary: "Set an issue's custom field values",
			Description: "A partial update, applied in one transaction: every key must be a visible field " +
				"(unknown_field, listing the unknown keys), and every value must parse for its field's kind " +
				"(bad_value naming the key and kind, bad_option listing the declared options); a null or empty " +
				"string clears the value, refused required_field when the field cannot be cleared. A refusal on " +
				"any key leaves every value, cleared or set, exactly as it stood. values may be sent as a JSON " +
				"object or as a JSON string holding one, since the CLI sends every body value as a string. " +
				"Authorized by Gitea's own write check on the Issues unit.",
			Tag: "fields", PathParams: issueParam,
			Body: append(append([]hubapi.Param{}, repoParam...),
				hubapi.Param{Name: "values", In: "body", Type: "string", Required: true, Description: "A JSON object (or a JSON string holding one) mapping field key to value; null or \"\" clears it."}),
			CLINames: []string{"issue-set-fields"},
			Response: "IssueFacets", ResponseIs: "object",
		},
		Handler: SetIssueFields,
	}
}

// valuesOrEmpty renders a missing values map as {} rather than null: an issue with no recorded
// value for anything still has fields, just none set.
func valuesOrEmpty(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	return values
}

// scopeListRead answers the repo_id/org_id-scoped list read GET /fields shares with GET
// /issue-types: bad_scope when both are given, the nearest scope's own rows via forRepo, an
// organization's plus the instance's via forOrg, or the instance's alone when neither is given.
func scopeListRead[T any](
	ctx *context.APIContext, spec query.Spec, readableRepo func(*context.APIContext, int64) (*repo_model.Repository, bool),
	forRepo func(stdcontext.Context, *repo_model.Repository) ([]T, error), forOrg func(stdcontext.Context, int64) ([]T, error),
) {
	q, ok := hubapi.ParseQuery(ctx, spec)
	if !ok {
		return
	}
	repoID := hubapi.EqualityFilterInt(q, "repo_id")
	orgID := hubapi.EqualityFilterInt(q, "org_id")
	if repoID > 0 && orgID > 0 {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_scope",
			"repo_id and org_id cannot both be given", "Send repo_id or org_id, never both.")
		return
	}

	var (
		rows []T
		err  error
	)
	switch {
	case repoID > 0:
		repo, ok := readableRepo(ctx, repoID)
		if !ok {
			return
		}
		rows, err = forRepo(ctx, repo)
	case orgID > 0:
		org, ok := issueTypeVisibleOrg(ctx, orgID)
		if !ok {
			return
		}
		rows, err = forOrg(ctx, org.ID)
	default:
		rows, err = forOrg(ctx, 0)
	}
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusOK, rows)
}

// GetFields answers GET /fields.
func GetFields(ctx *context.APIContext) {
	scopeListRead(ctx, fieldSpec, issueTypeReadableRepoNoPerm, planning_service.FieldsFor, planning_service.FieldsForOrg)
}

type fieldBody struct {
	RepoID   int64    `json:"repo_id"`
	OrgID    int64    `json:"org_id"`
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Kind     string   `json:"kind"`
	Options  []string `json:"options"`
	Required bool     `json:"required"`
	Sort     int      `json:"sort"`
}

func readFieldBody(ctx *context.APIContext) (*fieldBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(ctx.Req.Body, maxWriteBody+1))
	if err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "unreadable_body",
			"the request body could not be read", "Retry the request.")
		return nil, false
	}
	if len(raw) > maxWriteBody {
		hubapi.APIError(ctx, http.StatusBadRequest, "body_too_large",
			"the request body is larger than 16KiB", "A field is a handful of short fields; send only those.")
		return nil, false
	}
	body := new(fieldBody)
	if err := json.Unmarshal(raw, body); err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"repo_id": 1, "key": "points", "label": "Points", "kind": "int"}.`)
		return nil, false
	}
	return body, true
}

// CreateField answers POST /fields.
func CreateField(ctx *context.APIContext) {
	body, ok := readFieldBody(ctx)
	if !ok {
		return
	}
	row, err := planning_service.CreateField(ctx, ctx.Doer,
		planning_service.Scope{RepoID: body.RepoID, OrgID: body.OrgID},
		planning_service.FieldInput{Key: body.Key, Label: body.Label, Kind: body.Kind, Options: body.Options, Required: body.Required, Sort: body.Sort})
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
		return
	}
	ctx.JSON(http.StatusOK, fieldRowFrom(row))
}

// UpdateField answers PUT /fields/{id}.
func UpdateField(ctx *context.APIContext) {
	body, ok := readFieldBody(ctx)
	if !ok {
		return
	}
	row, err := planning_service.UpdateField(ctx, ctx.Doer, ctx.PathParamInt64("id"),
		planning_service.FieldInput{Key: body.Key, Label: body.Label, Kind: body.Kind, Options: body.Options, Required: body.Required, Sort: body.Sort})
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
		return
	}
	ctx.JSON(http.StatusOK, fieldRowFrom(row))
}

// DeleteField answers DELETE /fields/{id}.
func DeleteField(ctx *context.APIContext) {
	deleted, err := planning_service.DeleteField(ctx, ctx.Doer, ctx.PathParamInt64("id"))
	if err != nil {
		hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
		return
	}
	ctx.JSON(http.StatusOK, &FieldDeleteResult{DeletedValues: deleted})
}

// GetIssueFields answers GET /issues/{issue_id}/fields.
func GetIssueFields(ctx *context.APIContext) {
	issue, repo, ok := readableIssue(ctx)
	if !ok {
		return
	}
	fields, err := planning_service.FieldsFor(ctx, repo)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	values, err := planning_service.ValuesFor(ctx, repo, []int64{issue.ID})
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusOK, &IssueFields{Fields: fields, Values: valuesOrEmpty(values[issue.ID])})
}

// issueFieldsBody is PUT /issues/{issue_id}/fields' own body: values is read as either a JSON
// object or a JSON string holding one, since the CLI sends every body member as a string.
type issueFieldsBody struct {
	Repo   string `json:"repo"`
	Values any    `json:"values"`
}

func readIssueFieldsBody(ctx *context.APIContext) (*issueFieldsBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(ctx.Req.Body, maxWriteBody+1))
	if err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "unreadable_body",
			"the request body could not be read", "Retry the request.")
		return nil, false
	}
	if len(raw) > maxWriteBody {
		hubapi.APIError(ctx, http.StatusBadRequest, "body_too_large",
			"the request body is larger than 16KiB", "Send only repo and values.")
		return nil, false
	}
	body := new(issueFieldsBody)
	if err := json.Unmarshal(raw, body); err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"repo": "acme/widgets", "values": {"points": 5}}.`)
		return nil, false
	}
	return body, true
}

// parseFieldsValues resolves the wire's dual shape for values into the object SetIssueFields
// reads: a real JSON object, or a JSON string holding one.
func parseFieldsValues(ctx *context.APIContext, raw any) (map[string]any, bool) {
	switch v := raw.(type) {
	case nil:
		return map[string]any{}, true
	case map[string]any:
		return v, true
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return map[string]any{}, true
		}
		obj := map[string]any{}
		if err := json.Unmarshal([]byte(s), &obj); err != nil {
			hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
				"values is not a JSON object, nor a JSON string holding one",
				`Send values as {"points": 5}, or as the string "{\"points\": 5}".`)
			return nil, false
		}
		return obj, true
	default:
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
			"values is not a JSON object, nor a JSON string holding one",
			`Send values as {"points": 5}, or as the string "{\"points\": 5}".`)
		return nil, false
	}
}

// issueFieldsTarget resolves the repository and issue a fields write names, with the same
// visibility-then-write check every issue-scoped write in this area applies.
func issueFieldsTarget(ctx *context.APIContext) (*issueFieldsBody, *issues_model.Issue, bool) {
	body, ok := readIssueFieldsBody(ctx)
	if !ok {
		return nil, nil, false
	}
	owner, name, found := strings.Cut(strings.TrimSpace(body.Repo), "/")
	if !found || owner == "" || name == "" {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_repo",
			"repo must be owner/name, got "+body.Repo,
			"Send repo as owner/name, for example \"acme/widgets\".")
		return nil, nil, false
	}
	repo, err := repo_model.GetRepositoryByOwnerAndName(ctx, owner, name)
	if err != nil {
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository "+owner+"/"+name+" is visible to you",
			"Check the owner and repository name against "+BasePath+"/repos.")
		return nil, nil, false
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, nil, false
	}
	if !perm.CanRead(unit.TypeIssues) {
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository "+owner+"/"+name+" is visible to you",
			"Check the owner and repository name against "+BasePath+"/repos.")
		return nil, nil, false
	}
	if !perm.CanWrite(unit.TypeIssues) {
		hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
			"your account has no write access to the Issues unit of "+repo.FullName(),
			"Ask a repository administrator for write permission on Issues.")
		return nil, nil, false
	}
	issue, err := issues_model.GetIssueByID(ctx, ctx.PathParamInt64("issue_id"))
	if err != nil || issue.RepoID != repo.ID {
		hubapi.APIError(ctx, http.StatusNotFound, "issue_not_found",
			"no issue with that id belongs to "+repo.FullName(),
			"The path takes the issue's global id, not its per-repository number; "+BasePath+"/roadmap publishes issue_id on every bar.")
		return nil, nil, false
	}
	issue.Repo = repo
	if err := issue.LoadAttributes(ctx); err != nil {
		ctx.APIErrorInternal(err)
		return nil, nil, false
	}
	return body, issue, true
}

// SetIssueFields answers PUT /issues/{issue_id}/fields.
func SetIssueFields(ctx *context.APIContext) {
	body, issue, ok := issueFieldsTarget(ctx)
	if !ok {
		return
	}
	values, ok := parseFieldsValues(ctx, body.Values)
	if !ok {
		return
	}
	if err := planning_service.SetIssueFields(ctx, issue, values); err != nil {
		hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
		return
	}
	renderIssueFacets(ctx, issue)
}
