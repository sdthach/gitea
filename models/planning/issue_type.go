// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"context"
	"strings"

	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"

	"xorm.io/builder"
)

// IssueType is one work-item type, scoped to a repository, an organization or the whole
// instance (RepoID 0 and OrgID 0), visible everywhere nothing narrower shadows it.
type IssueType struct {
	ID          int64              `xorm:"pk autoincr"`
	RepoID      int64              `xorm:"UNIQUE(scope_name) INDEX NOT NULL DEFAULT 0"`
	OrgID       int64              `xorm:"UNIQUE(scope_name) INDEX NOT NULL DEFAULT 0"`
	Name        string             `xorm:"UNIQUE(scope_name) NOT NULL"`
	Color       string             `xorm:"NOT NULL"`
	Icon        string             `xorm:"NOT NULL"`
	Rank        int                `xorm:"NOT NULL DEFAULT 3"`
	Sort        int                `xorm:"NOT NULL DEFAULT 0"`
	CreatedUnix timeutil.TimeStamp `xorm:"created NOT NULL"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated NOT NULL"`
}

func (*IssueType) TableName() string { return "plan_issue_type" }

// IssueTypeAssignment is the one type an issue carries. IssueID is unique: an issue has at
// most one type, so a new assignment replaces rather than adds.
type IssueTypeAssignment struct {
	ID          int64              `xorm:"pk autoincr"`
	IssueID     int64              `xorm:"UNIQUE NOT NULL"`
	TypeID      int64              `xorm:"INDEX NOT NULL"`
	CreatedUnix timeutil.TimeStamp `xorm:"created NOT NULL"`
}

func (*IssueTypeAssignment) TableName() string { return "plan_issue_type_assignment" }

func init() {
	db.RegisterModel(new(IssueType))
	db.RegisterModel(new(IssueTypeAssignment))
}

// normalizeTypeName is the one place a name is lower-cased before it reaches a row, so the
// invariant holds regardless of what a caller already did to it.
func normalizeTypeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// TypesInScopes returns every type visible from repoID/orgID in one query: the instance scope,
// orgID's own, and repoID's own. Shadowing by name between them is the caller's concern.
func TypesInScopes(ctx context.Context, repoID, orgID int64) ([]*IssueType, error) {
	var cond builder.Cond = builder.Eq{"repo_id": 0, "org_id": 0}
	if orgID > 0 {
		cond = builder.Or(cond, builder.Eq{"repo_id": 0, "org_id": orgID})
	}
	if repoID > 0 {
		cond = builder.Or(cond, builder.Eq{"repo_id": repoID, "org_id": 0})
	}
	rows := make([]*IssueType, 0, 8)
	err := db.GetEngine(ctx).Where(cond).OrderBy("rank ASC, sort ASC, name ASC").Find(&rows)
	return rows, err
}

// GetIssueType reads one type by id, util.ErrNotExist when there is none.
func GetIssueType(ctx context.Context, id int64) (*IssueType, error) {
	row := new(IssueType)
	has, err := db.GetEngine(ctx).ID(id).Get(row)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, util.ErrNotExist
	}
	return row, nil
}

// GetIssueTypesByIDs batch-reads types, for callers resolving many assignments' names, colours
// and icons at once. An id with no row is simply absent from the result.
func GetIssueTypesByIDs(ctx context.Context, ids []int64) (map[int64]*IssueType, error) {
	out := map[int64]*IssueType{}
	if len(ids) == 0 {
		return out, nil
	}
	rows := make([]*IssueType, 0, len(ids))
	if err := db.GetEngine(ctx).In("id", ids).Find(&rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = row
	}
	return out, nil
}

// TypeExists reports whether repoID/orgID already has a type of that name, excluding excludeID
// (0 excludes nothing), so an update can check uniqueness against every OTHER row in its scope.
func TypeExists(ctx context.Context, repoID, orgID int64, name string, excludeID int64) (bool, error) {
	sess := db.GetEngine(ctx).Where("repo_id = ? AND org_id = ? AND name = ?", repoID, orgID, normalizeTypeName(name))
	if excludeID > 0 {
		sess = sess.And("id != ?", excludeID)
	}
	return sess.Exist(new(IssueType))
}

// InsertIssueType creates a type, lower-casing its name regardless of what the caller already did.
func InsertIssueType(ctx context.Context, row *IssueType) error {
	row.Name = normalizeTypeName(row.Name)
	return db.Insert(ctx, row)
}

// UpdateIssueType replaces a type's editable fields: name, colour, icon and rank. Scope is
// fixed at creation and never rewritten here.
func UpdateIssueType(ctx context.Context, row *IssueType) error {
	row.Name = normalizeTypeName(row.Name)
	_, err := db.GetEngine(ctx).ID(row.ID).Cols("name", "color", "icon", "rank").Update(row)
	return err
}

// DeleteIssueType removes a type row. Callers decide whether its assignments must go first.
func DeleteIssueType(ctx context.Context, id int64) error {
	_, err := db.GetEngine(ctx).ID(id).Delete(new(IssueType))
	return err
}

// AssignmentsFor reads every recorded assignment among issueIDs. An id with no row is simply
// absent from the result, which every caller reads as "no type assigned".
func AssignmentsFor(ctx context.Context, issueIDs []int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	if len(issueIDs) == 0 {
		return out, nil
	}
	rows := make([]*IssueTypeAssignment, 0, len(issueIDs))
	if err := db.GetEngine(ctx).In("issue_id", issueIDs).Find(&rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.IssueID] = row.TypeID
	}
	return out, nil
}

// IssueIDsForType lists every issue currently assigned typeID, for a caller filtering issues
// by their assigned type — for instance, every issue whose type is named epic.
func IssueIDsForType(ctx context.Context, typeID int64) ([]int64, error) {
	ids := make([]int64, 0, 16)
	err := db.GetEngine(ctx).Table("plan_issue_type_assignment").Where("type_id = ?", typeID).Cols("issue_id").Find(&ids)
	return ids, err
}

// UpsertAssignment records issueID's type, replacing any previous value: an issue carries at
// most one type.
func UpsertAssignment(ctx context.Context, issueID, typeID int64) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		row := new(IssueTypeAssignment)
		has, err := db.GetEngine(ctx).Where("issue_id = ?", issueID).Get(row)
		if err != nil {
			return err
		}
		if has {
			row.TypeID = typeID
			_, err = db.GetEngine(ctx).ID(row.ID).Cols("type_id").Update(row)
			return err
		}
		return db.Insert(ctx, &IssueTypeAssignment{IssueID: issueID, TypeID: typeID})
	})
}

// DeleteAssignment removes issueID's recorded type, if any.
func DeleteAssignment(ctx context.Context, issueID int64) error {
	_, err := db.GetEngine(ctx).Where("issue_id = ?", issueID).Delete(new(IssueTypeAssignment))
	return err
}

// DeleteAssignmentsForType removes every assignment naming typeID, for a forced delete that
// clears a type still in use along with it.
func DeleteAssignmentsForType(ctx context.Context, typeID int64) error {
	_, err := db.GetEngine(ctx).Where("type_id = ?", typeID).Delete(new(IssueTypeAssignment))
	return err
}

// CountAssignments counts how many issues currently carry typeID, so a delete can refuse one
// still in use and say how many.
func CountAssignments(ctx context.Context, typeID int64) (int64, error) {
	return db.GetEngine(ctx).Where("type_id = ?", typeID).Count(new(IssueTypeAssignment))
}
