// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"gitea.dev/models/db"
	hub_model "gitea.dev/models/hub"
	issues_model "gitea.dev/models/issues"
	org_model "gitea.dev/models/organization"
	access_model "gitea.dev/models/perm/access"
	planning_model "gitea.dev/models/planning"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/svg"
	"gitea.dev/modules/util"
)

// The three scopes a type may live in, published so a client renders one without inventing
// its own spelling.
const (
	ScopeInstance = "instance"
	ScopeOrg      = "org"
	ScopeRepo     = "repo"
)

// Scope names where a type write applies: a repository, an organization, or — both zero —
// the whole instance.
type Scope struct {
	RepoID int64
	OrgID  int64
}

// TypeInput is a type's editable fields, common to creating and updating one.
type TypeInput struct {
	Name  string
	Color string
	Icon  string
	Rank  int
}

// VisibleType is one type as TypesFor publishes it: the row plus the scope it was read from,
// so a client can show where an edit would apply.
type VisibleType struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Color   string `json:"color"`
	Icon    string `json:"icon"`
	Rank    int    `json:"rank"`
	Sort    int    `json:"sort"`
	Scope   string `json:"scope"`
	ScopeID int64  `json:"scope_id"`
}

// AssignedType is the type behind one issue's assignment, reduced to what a card or bar draws.
type AssignedType struct {
	TypeID int64  `json:"type_id"`
	Name   string `json:"name"`
	Color  string `json:"color"`
	Icon   string `json:"icon"`
}

// TypesFor reads every type visible from repo: its own, its organization's when it belongs to
// one, and the instance's, nearest scope shadowing by name so a repository never sees two rows
// answering to the same name.
func TypesFor(ctx context.Context, repo *repo_model.Repository) ([]VisibleType, error) {
	orgID, err := repoOrgID(ctx, repo)
	if err != nil {
		return nil, err
	}
	rows, err := planning_model.TypesInScopes(ctx, repo.ID, orgID)
	if err != nil {
		return nil, err
	}
	return shadowTypes(rows, repo.ID, orgID), nil
}

// TypesForOrg reads an organization's own types and the instance's, the pair GET
// /issue-types?org_id publishes.
func TypesForOrg(ctx context.Context, orgID int64) ([]VisibleType, error) {
	rows, err := planning_model.TypesInScopes(ctx, 0, orgID)
	if err != nil {
		return nil, err
	}
	return shadowTypes(rows, 0, orgID), nil
}

// repoOrgID is the organization a repository belongs to, 0 for a personal repository.
func repoOrgID(ctx context.Context, repo *repo_model.Repository) (int64, error) {
	if err := repo.LoadOwner(ctx); err != nil {
		return 0, err
	}
	if repo.Owner != nil && repo.Owner.IsOrganization() {
		return repo.OwnerID, nil
	}
	return 0, nil
}

// shadowTypes keeps, for each name, the nearest-scope row — repo over org over instance — and
// sorts the result by rank, then sort, then name, so a board and a picker render the same order.
func shadowTypes(rows []*planning_model.IssueType, repoID, orgID int64) []VisibleType {
	nearness := func(row *planning_model.IssueType) int {
		switch {
		case repoID > 0 && row.RepoID == repoID:
			return 0
		case orgID > 0 && row.OrgID == orgID:
			return 1
		default:
			return 2
		}
	}
	best := map[string]*planning_model.IssueType{}
	for _, row := range rows {
		if cur, ok := best[row.Name]; !ok || nearness(row) < nearness(cur) {
			best[row.Name] = row
		}
	}
	out := make([]VisibleType, 0, len(best))
	for _, row := range best {
		scope, scopeID := ScopeInstance, int64(0)
		switch {
		case row.RepoID > 0:
			scope, scopeID = ScopeRepo, row.RepoID
		case row.OrgID > 0:
			scope, scopeID = ScopeOrg, row.OrgID
		}
		out = append(out, VisibleType{
			ID: row.ID, Name: row.Name, Color: row.Color, Icon: row.Icon, Rank: row.Rank, Sort: row.Sort,
			Scope: scope, ScopeID: scopeID,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		if out[i].Sort != out[j].Sort {
			return out[i].Sort < out[j].Sort
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// typeNamePattern is checked after lower-casing: a letter or digit, then any run of letters,
// digits, spaces, underscores or hyphens.
var typeNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9 _-]*$`)

// validateTypeName lower-cases and checks a candidate name, which is where storage's own
// invariant — a type's name is always lower-case — is enforced before a row is ever built.
func validateTypeName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" || len(name) > 50 || !typeNamePattern.MatchString(name) {
		return "", &hub_model.Error{
			Code: "bad_type_name", Status: http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("%q is not a type name this endpoint accepts", raw),
			SuggestedAction: "Use 1-50 characters starting with a letter or digit, followed by letters, digits, " +
				"spaces, underscores or hyphens.",
		}
	}
	return name, nil
}

// validateIcon checks icon against the svg registry: svg.RenderHTML answers an unknown name
// with a `<span>` placeholder rather than an error, so "starts with <svg" is what this checks.
func validateIcon(icon string) error {
	if !strings.HasPrefix(string(svg.RenderHTML(icon)), "<svg") {
		return &hub_model.Error{
			Code: "bad_icon", Status: http.StatusUnprocessableEntity,
			Message:         fmt.Sprintf("%q is not a known icon name", icon),
			SuggestedAction: "Use one of the octicon-* names shipped under public/assets/img/svg, such as octicon-issue-opened.",
		}
	}
	return nil
}

// validateRank checks rank against the declared range: 1 is the highest a type may claim, 9
// the lowest.
func validateRank(rank int) error {
	if rank < 1 || rank > 9 {
		return &hub_model.Error{
			Code: "bad_rank", Status: http.StatusUnprocessableEntity,
			Message:         fmt.Sprintf("rank %d is out of range", rank),
			SuggestedAction: "Send a rank between 1 (highest) and 9 (lowest).",
		}
	}
	return nil
}

// validateScope refuses a scope naming both a repository and an organization, and an
// instance scope from anyone but a site administrator.
func validateScope(scope Scope, doer *user_model.User) error {
	switch {
	case scope.RepoID > 0 && scope.OrgID > 0:
		return &hub_model.Error{
			Code: "bad_scope", Status: http.StatusUnprocessableEntity,
			Message:         "a type cannot be scoped to both a repository and an organization",
			SuggestedAction: "Send repo_id or org_id, never both.",
		}
	case scope.RepoID == 0 && scope.OrgID == 0 && !doer.IsAdmin:
		return &hub_model.Error{
			Code: "bad_scope", Status: http.StatusUnprocessableEntity,
			Message: "only a site administrator may create or change an instance-scoped type",
			SuggestedAction: "Send repo_id or org_id to scope the type to a repository or organization you administer, " +
				"or ask a site administrator for an instance-wide one.",
		}
	}
	return nil
}

// scopeAdmin is the administrator check for scope: repository admin for a repo scope,
// organization owner for an org scope, site admin for the instance scope.
func scopeAdmin(ctx context.Context, doer *user_model.User, scope Scope) (bool, error) {
	switch {
	case scope.RepoID > 0:
		repo, err := repo_model.GetRepositoryByID(ctx, scope.RepoID)
		if err != nil {
			return false, err
		}
		perm, err := access_model.GetDoerRepoPermission(ctx, repo, doer)
		if err != nil {
			return false, err
		}
		return perm.IsAdmin(), nil
	case scope.OrgID > 0:
		org, err := org_model.GetOrgByID(ctx, scope.OrgID)
		if err != nil {
			return false, err
		}
		return org.IsOwnedBy(ctx, doer.ID)
	default:
		return doer.IsAdmin, nil
	}
}

var errTypeForbidden = &hub_model.Error{
	Code: "forbidden", Status: http.StatusForbidden,
	Message:         "you do not administer this type's scope",
	SuggestedAction: "Ask a repository administrator, an organization owner, or a site administrator to make this change.",
}

var errTypeNotFound = &hub_model.Error{
	Code: "not_found", Status: http.StatusNotFound,
	Message:         "no issue type with that id exists",
	SuggestedAction: "List the types this repository or organization can see and use one of their ids.",
}

func typeExistsError(name string) error {
	return &hub_model.Error{
		Code: "type_exists", Status: http.StatusUnprocessableEntity,
		Message:         fmt.Sprintf("a type named %q already exists in this scope", name),
		SuggestedAction: "Choose a different name, or update the existing type instead of creating a new one.",
	}
}

// validateTypeInput runs every stateless check shared by create and update, in the order a
// caller can fix them one at a time.
func validateTypeInput(in TypeInput) (string, error) {
	name, err := validateTypeName(in.Name)
	if err != nil {
		return "", err
	}
	if err := validateIcon(in.Icon); err != nil {
		return "", err
	}
	if err := validateRank(in.Rank); err != nil {
		return "", err
	}
	return name, nil
}

// CreateType creates a type in scope, refusing a bad scope, a caller who does not administer
// it, a bad name, icon or rank, or a name already taken in that scope.
func CreateType(ctx context.Context, doer *user_model.User, scope Scope, in TypeInput) (*planning_model.IssueType, error) {
	if err := validateScope(scope, doer); err != nil {
		return nil, err
	}
	ok, err := scopeAdmin(ctx, doer, scope)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errTypeForbidden
	}
	name, err := validateTypeInput(in)
	if err != nil {
		return nil, err
	}
	exists, err := planning_model.TypeExists(ctx, scope.RepoID, scope.OrgID, name, 0)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, typeExistsError(name)
	}
	row := &planning_model.IssueType{
		RepoID: scope.RepoID, OrgID: scope.OrgID, Name: name, Color: in.Color, Icon: in.Icon, Rank: in.Rank,
	}
	if err := planning_model.InsertIssueType(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

// UpdateType replaces a type's editable fields. Scope is fixed at creation, so this checks
// admin over the type's OWN scope rather than one the caller names.
func UpdateType(ctx context.Context, doer *user_model.User, id int64, in TypeInput) (*planning_model.IssueType, error) {
	row, err := planning_model.GetIssueType(ctx, id)
	if err != nil {
		return nil, typeLookupError(err)
	}
	ok, err := scopeAdmin(ctx, doer, Scope{RepoID: row.RepoID, OrgID: row.OrgID})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errTypeForbidden
	}
	name, err := validateTypeInput(in)
	if err != nil {
		return nil, err
	}
	exists, err := planning_model.TypeExists(ctx, row.RepoID, row.OrgID, name, row.ID)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, typeExistsError(name)
	}
	row.Name, row.Color, row.Icon, row.Rank = name, in.Color, in.Icon, in.Rank
	if err := planning_model.UpdateIssueType(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

// DeleteType removes a type, refusing one still assigned unless force is set, in which case
// its assignments are cleared along with it. It returns the row as it stood just before
// deletion, so a caller can confirm what was removed.
func DeleteType(ctx context.Context, doer *user_model.User, id int64, force bool) (*planning_model.IssueType, error) {
	row, err := planning_model.GetIssueType(ctx, id)
	if err != nil {
		return nil, typeLookupError(err)
	}
	ok, err := scopeAdmin(ctx, doer, Scope{RepoID: row.RepoID, OrgID: row.OrgID})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errTypeForbidden
	}
	count, err := planning_model.CountAssignments(ctx, id)
	if err != nil {
		return nil, err
	}
	if count > 0 && !force {
		return nil, &hub_model.Error{
			Code: "type_in_use", Status: http.StatusConflict,
			Message: fmt.Sprintf("%d issue(s) still carry this type", count),
			SuggestedAction: "Reassign those issues first, or repeat the request with force=true " +
				"to delete the type and clear its assignments.",
		}
	}
	err = db.WithTx(ctx, func(ctx context.Context) error {
		if count > 0 {
			if err := planning_model.DeleteAssignmentsForType(ctx, id); err != nil {
				return err
			}
		}
		return planning_model.DeleteIssueType(ctx, id)
	})
	if err != nil {
		return nil, err
	}
	return row, nil
}

func typeLookupError(err error) error {
	if errors.Is(err, util.ErrNotExist) {
		return errTypeNotFound
	}
	return err
}

// SetIssueType assigns issue the type named by typeID, refusing one not visible from the
// issue's own repository — issue.Repo must already be loaded.
func SetIssueType(ctx context.Context, issue *issues_model.Issue, typeID int64) error {
	types, err := TypesFor(ctx, issue.Repo)
	if err != nil {
		return err
	}
	for _, t := range types {
		if t.ID == typeID {
			return planning_model.UpsertAssignment(ctx, issue.ID, typeID)
		}
	}
	return &hub_model.Error{
		Code: "type_not_visible", Status: http.StatusUnprocessableEntity,
		Message:         "that type is not visible from this issue's repository",
		SuggestedAction: "Use one of the ids GET /issue-types?repo_id=<repo_id> returns for this repository.",
	}
}

// ClearIssueType removes issueID's recorded type, if any.
func ClearIssueType(ctx context.Context, issueID int64) error {
	return planning_model.DeleteAssignment(ctx, issueID)
}

// IssueIDsForType lists every issue currently assigned typeID — the roadmap's zoom=epic
// reading: issues whose assigned type is named epic.
func IssueIDsForType(ctx context.Context, typeID int64) ([]int64, error) {
	return planning_model.IssueIDsForType(ctx, typeID)
}

// Assignments reads every recorded assignment among issueIDs, joined with the type it names —
// its name, colour and icon, which is what a card or a bar draws. An id with no assignment, or
// whose type has since been deleted, is simply absent from the result.
func Assignments(ctx context.Context, issueIDs []int64) (map[int64]AssignedType, error) {
	byIssue, err := planning_model.AssignmentsFor(ctx, issueIDs)
	if err != nil {
		return nil, err
	}
	typeIDs := make([]int64, 0, len(byIssue))
	seen := map[int64]bool{}
	for _, typeID := range byIssue {
		if !seen[typeID] {
			seen[typeID] = true
			typeIDs = append(typeIDs, typeID)
		}
	}
	types, err := planning_model.GetIssueTypesByIDs(ctx, typeIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]AssignedType, len(byIssue))
	for issueID, typeID := range byIssue {
		row, ok := types[typeID]
		if !ok {
			continue
		}
		out[issueID] = AssignedType{TypeID: typeID, Name: row.Name, Color: row.Color, Icon: row.Icon}
	}
	return out, nil
}
