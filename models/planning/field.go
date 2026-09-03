// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"context"

	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"

	"xorm.io/builder"
)

// Field is one custom field, scoped to a repository, an organization or the whole instance
// (RepoID 0 and OrgID 0), visible everywhere nothing narrower shadows it by key.
type Field struct {
	ID     int64 `xorm:"pk autoincr"`
	RepoID int64 `xorm:"UNIQUE(scope_key) INDEX NOT NULL DEFAULT 0"`
	OrgID  int64 `xorm:"UNIQUE(scope_key) INDEX NOT NULL DEFAULT 0"`
	// Key is stored under the column name field_key: KEY is a reserved word in MySQL, and a
	// bare "key" column would need per-dialect quoting everywhere it is read or written.
	Key         string             `xorm:"'field_key' UNIQUE(scope_key) NOT NULL"`
	Label       string             `xorm:"NOT NULL"`
	Kind        string             `xorm:"NOT NULL"`
	Options     []string           `xorm:"JSON TEXT"`
	Required    bool               `xorm:"NOT NULL DEFAULT false"`
	Sort        int                `xorm:"NOT NULL DEFAULT 0"`
	CreatedUnix timeutil.TimeStamp `xorm:"created NOT NULL"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated NOT NULL"`
}

func (*Field) TableName() string { return "plan_field" }

// FieldValue is one issue's recorded value for one field. Only the column matching the
// field's own kind is ever read; the others sit at their zero value.
type FieldValue struct {
	ID          int64              `xorm:"pk autoincr"`
	IssueID     int64              `xorm:"UNIQUE(issue_field) INDEX NOT NULL"`
	FieldID     int64              `xorm:"UNIQUE(issue_field) INDEX NOT NULL"`
	ValueInt    int64              `xorm:"NOT NULL DEFAULT 0"`
	ValueText   string             `xorm:"TEXT NOT NULL DEFAULT ''"`
	ValueUnix   timeutil.TimeStamp `xorm:"NOT NULL DEFAULT 0"`
	CreatedUnix timeutil.TimeStamp `xorm:"created NOT NULL"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated NOT NULL"`
}

func (*FieldValue) TableName() string { return "plan_field_value" }

func init() {
	db.RegisterModel(new(Field))
	db.RegisterModel(new(FieldValue))
}

// FieldsInScopes returns every field visible from repoID/orgID in one query: the instance
// scope, orgID's own, and repoID's own. Shadowing by key between them is the caller's concern.
func FieldsInScopes(ctx context.Context, repoID, orgID int64) ([]*Field, error) {
	var cond builder.Cond = builder.Eq{"repo_id": 0, "org_id": 0}
	if orgID > 0 {
		cond = builder.Or(cond, builder.Eq{"repo_id": 0, "org_id": orgID})
	}
	if repoID > 0 {
		cond = builder.Or(cond, builder.Eq{"repo_id": repoID, "org_id": 0})
	}
	rows := make([]*Field, 0, 8)
	err := db.GetEngine(ctx).Where(cond).OrderBy("sort ASC, field_key ASC").Find(&rows)
	return rows, err
}

// GetField reads one field by id, util.ErrNotExist when there is none.
func GetField(ctx context.Context, id int64) (*Field, error) {
	row := new(Field)
	has, err := db.GetEngine(ctx).ID(id).Get(row)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, util.ErrNotExist
	}
	return row, nil
}

// FieldExists reports whether repoID/orgID already has a field of that key, excluding
// excludeID (0 excludes nothing), so an update can check uniqueness against every OTHER row
// in its scope.
func FieldExists(ctx context.Context, repoID, orgID int64, key string, excludeID int64) (bool, error) {
	sess := db.GetEngine(ctx).Where("repo_id = ? AND org_id = ? AND field_key = ?", repoID, orgID, key)
	if excludeID > 0 {
		sess = sess.And("id != ?", excludeID)
	}
	return sess.Exist(new(Field))
}

// InsertField creates a field.
func InsertField(ctx context.Context, row *Field) error {
	return db.Insert(ctx, row)
}

// UpdateField replaces a field's editable columns. Scope is fixed at creation and never
// rewritten here.
func UpdateField(ctx context.Context, row *Field) error {
	_, err := db.GetEngine(ctx).ID(row.ID).Cols("field_key", "label", "kind", "options", "required", "sort").Update(row)
	return err
}

// DeleteField removes a field and every recorded value naming it, in one transaction, and
// returns how many values were cascaded away.
func DeleteField(ctx context.Context, id int64) (int64, error) {
	var count int64
	err := db.WithTx(ctx, func(ctx context.Context) error {
		var err error
		count, err = db.GetEngine(ctx).Where("field_id = ?", id).Delete(new(FieldValue))
		if err != nil {
			return err
		}
		_, err = db.GetEngine(ctx).ID(id).Delete(new(Field))
		return err
	})
	return count, err
}

// ValuesFor batch-reads every recorded value among issueIDs, keyed by issue then by field. An
// issue with no recorded value for a field is simply absent from its inner map.
func ValuesFor(ctx context.Context, issueIDs []int64) (map[int64]map[int64]FieldValue, error) {
	out := map[int64]map[int64]FieldValue{}
	if len(issueIDs) == 0 {
		return out, nil
	}
	rows := make([]*FieldValue, 0, len(issueIDs))
	if err := db.GetEngine(ctx).In("issue_id", issueIDs).Find(&rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		byField, ok := out[row.IssueID]
		if !ok {
			byField = map[int64]FieldValue{}
			out[row.IssueID] = byField
		}
		byField[row.FieldID] = *row
	}
	return out, nil
}

// UpsertValue records one issue's value for one field, replacing any previous row for that
// (issue_id, field_id) pair.
func UpsertValue(ctx context.Context, row *FieldValue) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		existing := new(FieldValue)
		has, err := db.GetEngine(ctx).Where("issue_id = ? AND field_id = ?", row.IssueID, row.FieldID).Get(existing)
		if err != nil {
			return err
		}
		if has {
			existing.ValueInt, existing.ValueText, existing.ValueUnix = row.ValueInt, row.ValueText, row.ValueUnix
			_, err = db.GetEngine(ctx).ID(existing.ID).Cols("value_int", "value_text", "value_unix").Update(existing)
			return err
		}
		return db.Insert(ctx, row)
	})
}

// DeleteValue removes one issue's recorded value for one field, if any.
func DeleteValue(ctx context.Context, issueID, fieldID int64) error {
	_, err := db.GetEngine(ctx).Where("issue_id = ? AND field_id = ?", issueID, fieldID).Delete(new(FieldValue))
	return err
}
