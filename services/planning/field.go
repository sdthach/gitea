// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitea.dev/models/db"
	hub_model "gitea.dev/models/hub"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"
)

// The field kinds a custom field may declare.
const (
	FieldKindInt    = "int"
	FieldKindText   = "text"
	FieldKindDate   = "date"
	FieldKindSelect = "select"
)

// FieldKinds is the accepted set, in declaration order.
var FieldKinds = []string{FieldKindInt, FieldKindText, FieldKindDate, FieldKindSelect}

// maxFieldTextBytes is the text kind's own limit: 4 KiB, checked in bytes.
const maxFieldTextBytes = 4 << 10

// maxFieldOptions and maxFieldOptionLength bound a select field's declared options.
const (
	maxFieldOptions       = 50
	maxFieldOptionLength  = 100
	maxFieldLabelLength   = 100
	fieldKeyPatternSource = `^[a-z][a-z0-9_]{0,39}$`
)

// fieldKeyPattern is the slug a field's key must match.
var fieldKeyPattern = regexp.MustCompile(fieldKeyPatternSource)

// FieldInput is a field's editable fields, common to creating and updating one.
type FieldInput struct {
	Key      string
	Label    string
	Kind     string
	Options  []string
	Required bool
	Sort     int
}

// VisibleField is one field as FieldsFor publishes it: the row plus the scope it was read
// from, so a client can show where an edit would apply.
type VisibleField struct {
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

// FieldsFor reads every field visible from repo: its own, its organization's when it belongs
// to one, and the instance's, nearest scope shadowing by key so a repository never sees two
// rows answering to the same key.
func FieldsFor(ctx context.Context, repo *repo_model.Repository) ([]VisibleField, error) {
	orgID, err := repoOrgID(ctx, repo)
	if err != nil {
		return nil, err
	}
	rows, err := planning_model.FieldsInScopes(ctx, repo.ID, orgID)
	if err != nil {
		return nil, err
	}
	return shadowFields(rows, repo.ID, orgID), nil
}

// FieldsForOrg reads an organization's own fields and the instance's, the pair GET
// /fields?org_id publishes.
func FieldsForOrg(ctx context.Context, orgID int64) ([]VisibleField, error) {
	rows, err := planning_model.FieldsInScopes(ctx, 0, orgID)
	if err != nil {
		return nil, err
	}
	return shadowFields(rows, 0, orgID), nil
}

// shadowFields keeps, for each key, the nearest-scope row — repo over org over instance — and
// sorts the result by sort, then key, so a board and a picker render the same order.
func shadowFields(rows []*planning_model.Field, repoID, orgID int64) []VisibleField {
	nearness := func(row *planning_model.Field) int {
		switch {
		case repoID > 0 && row.RepoID == repoID:
			return 0
		case orgID > 0 && row.OrgID == orgID:
			return 1
		default:
			return 2
		}
	}
	best := map[string]*planning_model.Field{}
	for _, row := range rows {
		if cur, ok := best[row.Key]; !ok || nearness(row) < nearness(cur) {
			best[row.Key] = row
		}
	}
	out := make([]VisibleField, 0, len(best))
	for _, row := range best {
		scope, scopeID := ScopeInstance, int64(0)
		switch {
		case row.RepoID > 0:
			scope, scopeID = ScopeRepo, row.RepoID
		case row.OrgID > 0:
			scope, scopeID = ScopeOrg, row.OrgID
		}
		out = append(out, VisibleField{
			ID: row.ID, Key: row.Key, Label: row.Label, Kind: row.Kind, Options: row.Options,
			Required: row.Required, Sort: row.Sort, Scope: scope, ScopeID: scopeID,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sort != out[j].Sort {
			return out[i].Sort < out[j].Sort
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// validateFieldKey checks a candidate key against the slug every field key must match.
func validateFieldKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if !fieldKeyPattern.MatchString(key) {
		return "", &hub_model.Error{
			Code: "bad_key", Status: http.StatusUnprocessableEntity,
			Message:         fmt.Sprintf("%q is not a field key this endpoint accepts", raw),
			SuggestedAction: "Use a lower-case letter followed by up to 39 lower-case letters, digits or underscores.",
		}
	}
	return key, nil
}

// validateFieldLabel checks a candidate label: not empty, not over 100 characters.
func validateFieldLabel(raw string) (string, error) {
	label := strings.TrimSpace(raw)
	if label == "" || len(label) > maxFieldLabelLength {
		return "", &hub_model.Error{
			Code: "bad_label", Status: http.StatusUnprocessableEntity,
			Message:         fmt.Sprintf("%q is not a field label this endpoint accepts", raw),
			SuggestedAction: fmt.Sprintf("Use 1-%d characters.", maxFieldLabelLength),
		}
	}
	return label, nil
}

// validateFieldKind checks a candidate kind against the declared set.
func validateFieldKind(raw string) (string, error) {
	kind := strings.TrimSpace(strings.ToLower(raw))
	if !slices.Contains(FieldKinds, kind) {
		return "", &hub_model.Error{
			Code: "bad_kind", Status: http.StatusUnprocessableEntity,
			Message:         fmt.Sprintf("%q is not a field kind this endpoint accepts", raw),
			SuggestedAction: "Use one of " + strings.Join(FieldKinds, ", ") + ".",
		}
	}
	return kind, nil
}

// validateFieldOptions checks a select field's declared options: at least one, at most 50,
// each at most 100 characters, no duplicates. A non-select field carries no options at all,
// regardless of what was sent — options mean nothing outside select.
func validateFieldOptions(kind string, options []string) ([]string, error) {
	if kind != FieldKindSelect {
		return nil, nil
	}
	err := &hub_model.Error{
		Code: "options_required", Status: http.StatusUnprocessableEntity,
		Message:         "a select field needs 1-50 options, each at most 100 characters, with no duplicate",
		SuggestedAction: "Send options as a list of 1 to 50 distinct strings, each at most 100 characters.",
	}
	if len(options) < 1 || len(options) > maxFieldOptions {
		return nil, err
	}
	seen := make(map[string]bool, len(options))
	for _, option := range options {
		if len(option) > maxFieldOptionLength || seen[option] {
			return nil, err
		}
		seen[option] = true
	}
	return options, nil
}

// pointsKindError refuses a field keyed points that is not an int: rollups sum it as one.
var pointsKindError = &hub_model.Error{
	Code: "bad_kind", Status: http.StatusUnprocessableEntity,
	Message:         `a field keyed "points" must be kind int, since every rollup sums it`,
	SuggestedAction: "Use kind int, or choose a different key for this field.",
}

// validateFieldInput runs every stateless check shared by create and update, in the order a
// caller can fix them one at a time, and returns the validated key, label and kind. It does
// not check points against kind: on an update, that has to wait until after kind_immutable, so
// changing an existing points field's kind is refused for staying fixed rather than for the
// name reservation.
func validateFieldInput(in FieldInput) (key, label, kind string, err error) {
	key, err = validateFieldKey(in.Key)
	if err != nil {
		return "", "", "", err
	}
	label, err = validateFieldLabel(in.Label)
	if err != nil {
		return "", "", "", err
	}
	kind, err = validateFieldKind(in.Kind)
	if err != nil {
		return "", "", "", err
	}
	return key, label, kind, nil
}

// validatePointsKind refuses a field keyed points under any kind but int.
func validatePointsKind(key, kind string) error {
	if key == "points" && kind != FieldKindInt {
		return pointsKindError
	}
	return nil
}

var errFieldForbidden = &hub_model.Error{
	Code: "forbidden", Status: http.StatusForbidden,
	Message:         "you do not administer this field's scope",
	SuggestedAction: "Ask a repository administrator, an organization owner, or a site administrator to make this change.",
}

var errFieldNotFound = &hub_model.Error{
	Code: "not_found", Status: http.StatusNotFound,
	Message:         "no field with that id exists",
	SuggestedAction: "List the fields this repository or organization can see and use one of their ids.",
}

var errFieldKindImmutable = &hub_model.Error{
	Code: "kind_immutable", Status: http.StatusUnprocessableEntity,
	Message:         "a field's kind cannot be changed once created",
	SuggestedAction: "Delete this field and create a new one with the kind you want; existing values are cascaded away with it.",
}

func fieldExistsError(key string) error {
	return &hub_model.Error{
		Code: "field_exists", Status: http.StatusUnprocessableEntity,
		Message:         fmt.Sprintf("a field keyed %q already exists in this scope", key),
		SuggestedAction: "Choose a different key, or update the existing field instead of creating a new one.",
	}
}

func fieldLookupError(err error) error {
	if errors.Is(err, util.ErrNotExist) {
		return errFieldNotFound
	}
	return err
}

// CreateField creates a field in scope, refusing a bad scope, a caller who does not
// administer it, a bad key, label, kind or option list, or a key already taken in that scope.
func CreateField(ctx context.Context, doer *user_model.User, scope Scope, in FieldInput) (*planning_model.Field, error) {
	if err := validateScope(scope, doer, "field"); err != nil {
		return nil, err
	}
	ok, err := scopeAdmin(ctx, doer, scope)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errFieldForbidden
	}
	key, label, kind, err := validateFieldInput(in)
	if err != nil {
		return nil, err
	}
	if err := validatePointsKind(key, kind); err != nil {
		return nil, err
	}
	options, err := validateFieldOptions(kind, in.Options)
	if err != nil {
		return nil, err
	}
	exists, err := planning_model.FieldExists(ctx, scope.RepoID, scope.OrgID, key, 0)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, fieldExistsError(key)
	}
	row := &planning_model.Field{
		RepoID: scope.RepoID, OrgID: scope.OrgID, Key: key, Label: label, Kind: kind,
		Options: options, Required: in.Required, Sort: in.Sort,
	}
	if err := planning_model.InsertField(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

// UpdateField replaces a field's editable columns. Scope is fixed at creation, so this checks
// admin over the field's OWN scope rather than one the caller names; kind is fixed too, once
// values may already exist under it.
func UpdateField(ctx context.Context, doer *user_model.User, id int64, in FieldInput) (*planning_model.Field, error) {
	row, err := planning_model.GetField(ctx, id)
	if err != nil {
		return nil, fieldLookupError(err)
	}
	ok, err := scopeAdmin(ctx, doer, Scope{RepoID: row.RepoID, OrgID: row.OrgID})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errFieldForbidden
	}
	key, label, kind, err := validateFieldInput(in)
	if err != nil {
		return nil, err
	}
	if kind != row.Kind {
		return nil, errFieldKindImmutable
	}
	if err := validatePointsKind(key, kind); err != nil {
		return nil, err
	}
	options, err := validateFieldOptions(kind, in.Options)
	if err != nil {
		return nil, err
	}
	exists, err := planning_model.FieldExists(ctx, row.RepoID, row.OrgID, key, row.ID)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, fieldExistsError(key)
	}
	row.Key, row.Label, row.Options, row.Required, row.Sort = key, label, options, in.Required, in.Sort
	if err := planning_model.UpdateField(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

// DeleteField removes a field and every recorded value naming it, returning how many values
// were cascaded away.
func DeleteField(ctx context.Context, doer *user_model.User, id int64) (int64, error) {
	row, err := planning_model.GetField(ctx, id)
	if err != nil {
		return 0, fieldLookupError(err)
	}
	ok, err := scopeAdmin(ctx, doer, Scope{RepoID: row.RepoID, OrgID: row.OrgID})
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errFieldForbidden
	}
	return planning_model.DeleteField(ctx, id)
}

// fieldWrite is one field's resolved value, set under the field's own kind. Only the member
// matching that kind is ever populated. SetIssueFields' own caller decides clearing separately,
// before this is ever built.
type fieldWrite struct {
	fieldID   int64
	valueInt  int64
	valueText string
	valueUnix timeutil.TimeStamp
}

func badValueError(key, kind string) error {
	return &hub_model.Error{
		Code: "bad_value", Status: http.StatusUnprocessableEntity,
		Message: fmt.Sprintf("the value sent for %q does not parse as %s", key, kind),
		SuggestedAction: map[string]string{
			FieldKindInt:    "Send a number or a numeric string.",
			FieldKindDate:   "Send a YYYY-MM-DD date or an RFC 3339 timestamp.",
			FieldKindText:   fmt.Sprintf("Send a string of at most %d bytes.", maxFieldTextBytes),
			FieldKindSelect: "Send one of the field's declared options as a string.",
		}[kind],
	}
}

func badOptionError(key string, options []string) error {
	return &hub_model.Error{
		Code: "bad_option", Status: http.StatusUnprocessableEntity,
		Message:         fmt.Sprintf("the value sent for %q is not one of its declared options", key),
		SuggestedAction: "Send one of: " + strings.Join(options, ", "),
	}
}

func requiredFieldError(key string) error {
	return &hub_model.Error{
		Code: "required_field", Status: http.StatusUnprocessableEntity,
		Message:         fmt.Sprintf("%q is required and cannot be cleared", key),
		SuggestedAction: "Send a value for it, or make it optional first.",
	}
}

func unknownFieldError(keys []string) error {
	return &hub_model.Error{
		Code: "unknown_field", Status: http.StatusUnprocessableEntity,
		Message:         "not a visible field: " + strings.Join(keys, ", "),
		SuggestedAction: "List the fields visible from this issue's repository and use one of their keys.",
	}
}

// isClearValue reports whether raw clears the field's value: JSON null, or an empty (or
// whitespace-only) string.
func isClearValue(raw any) bool {
	if raw == nil {
		return true
	}
	s, ok := raw.(string)
	return ok && strings.TrimSpace(s) == ""
}

// fieldDateFormats are what a date value may be sent as.
var fieldDateFormats = []string{time.RFC3339, "2006-01-02"}

// parseFieldValue resolves one field's raw JSON value into the write it performs. isClearValue
// has already been checked by the caller for a value that clears the field instead.
func parseFieldValue(field VisibleField, raw any) (fieldWrite, error) {
	write := fieldWrite{fieldID: field.ID}
	switch field.Kind {
	case FieldKindInt:
		n, ok := parseFieldInt(raw)
		if !ok {
			return write, badValueError(field.Key, field.Kind)
		}
		write.valueInt = n
	case FieldKindDate:
		s, ok := raw.(string)
		if !ok {
			return write, badValueError(field.Key, field.Kind)
		}
		s = strings.TrimSpace(s)
		unix, ok := parseFieldDate(s)
		if !ok {
			return write, badValueError(field.Key, field.Kind)
		}
		write.valueUnix = timeutil.TimeStamp(unix)
	case FieldKindText:
		s, ok := raw.(string)
		if !ok || len(s) > maxFieldTextBytes {
			return write, badValueError(field.Key, field.Kind)
		}
		write.valueText = s
	case FieldKindSelect:
		s, ok := raw.(string)
		if !ok {
			return write, badValueError(field.Key, field.Kind)
		}
		if !slices.Contains(field.Options, s) {
			return write, badOptionError(field.Key, field.Options)
		}
		write.valueText = s
	}
	return write, nil
}

// parseFieldInt reads an int kind's value: a JSON number, or a numeric string, so the CLI's
// own string-typed flags still work.
func parseFieldInt(raw any) (int64, bool) {
	switch v := raw.(type) {
	case float64:
		return int64(v), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// parseFieldDate reads a date kind's value: YYYY-MM-DD or RFC 3339, both read as UTC.
func parseFieldDate(s string) (int64, bool) {
	for _, format := range fieldDateFormats {
		if at, err := time.Parse(format, s); err == nil {
			return at.UTC().Unix(), true
		}
	}
	return 0, false
}

// SetIssueFields applies a partial update of issue's custom field values in one transaction:
// every key must be a visible field, and every value must parse for its kind, before any of it
// is written — a refusal midway leaves every value, cleared or set, exactly as it stood.
//
// values comes off the wire as either a JSON object or a JSON string holding one; the caller
// (routers/api/planning/v1) resolves that before calling here.
func SetIssueFields(ctx context.Context, issue *issues_model.Issue, values map[string]any) error {
	fields, err := FieldsFor(ctx, issue.Repo)
	if err != nil {
		return err
	}
	byKey := make(map[string]VisibleField, len(fields))
	for _, f := range fields {
		byKey[f.Key] = f
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	unknown := make([]string, 0, len(keys))
	for _, k := range keys {
		if _, ok := byKey[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		return unknownFieldError(unknown)
	}

	// Each key is validated and written in the same pass, inside one transaction: a refusal on
	// a later key rolls back every write this call already made for an earlier one.
	return db.WithTx(ctx, func(ctx context.Context) error {
		for _, k := range keys {
			field := byKey[k]
			raw := values[k]
			if isClearValue(raw) {
				if field.Required {
					return requiredFieldError(field.Key)
				}
				if err := planning_model.DeleteValue(ctx, issue.ID, field.ID); err != nil {
					return err
				}
				continue
			}
			write, err := parseFieldValue(field, raw)
			if err != nil {
				return err
			}
			row := &planning_model.FieldValue{
				IssueID: issue.ID, FieldID: write.fieldID,
				ValueInt: write.valueInt, ValueText: write.valueText, ValueUnix: write.valueUnix,
			}
			if err := planning_model.UpsertValue(ctx, row); err != nil {
				return err
			}
		}
		return nil
	})
}

// ValuesFor reads every recorded value among issueIDs, typed by its field's own kind: int as a
// number, date as its unix timestamp, text and select as a string. A field since deleted is
// silently absent rather than surfaced as a stale value.
func ValuesFor(ctx context.Context, repo *repo_model.Repository, issueIDs []int64) (map[int64]map[string]any, error) {
	fields, err := FieldsFor(ctx, repo)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]VisibleField, len(fields))
	for _, f := range fields {
		byID[f.ID] = f
	}

	raw, err := planning_model.ValuesFor(ctx, issueIDs)
	if err != nil {
		return nil, err
	}

	out := make(map[int64]map[string]any, len(raw))
	for issueID, byField := range raw {
		vals := make(map[string]any, len(byField))
		for fieldID, fv := range byField {
			field, ok := byID[fieldID]
			if !ok {
				continue
			}
			switch field.Kind {
			case FieldKindInt:
				vals[field.Key] = fv.ValueInt
			case FieldKindDate:
				vals[field.Key] = int64(fv.ValueUnix)
			case FieldKindText, FieldKindSelect:
				vals[field.Key] = fv.ValueText
			}
		}
		out[issueID] = vals
	}
	return out, nil
}

// PointsOf reads the points accessor a ValuesFor result carries for one issue, 0 when the
// nearest-scope field keyed points, always int, is unset, absent, or not visible from this
// repository — the rollup definition every parent, milestone and board group total reads.
func PointsOf(values map[string]any) int {
	if n, ok := values["points"].(int64); ok {
		return int(n)
	}
	return 0
}
