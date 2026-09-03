// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"context"
	"strings"

	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"
)

// ProjectView is a saved filter/sort over one project's board or roadmap: a name plus the
// query string the page reads it through. The query's grammar is the caller's concern; this
// row only carries it.
type ProjectView struct {
	ID          int64              `xorm:"pk autoincr"`
	ProjectID   int64              `xorm:"UNIQUE(project_name) INDEX NOT NULL"`
	Name        string             `xorm:"UNIQUE(project_name) NOT NULL"`
	Query       string             `xorm:"TEXT NOT NULL"`
	CreatedBy   int64              `xorm:"NOT NULL"`
	CreatedUnix timeutil.TimeStamp `xorm:"created NOT NULL"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated NOT NULL"`
}

func (*ProjectView) TableName() string { return "plan_project_view" }

func init() {
	db.RegisterModel(new(ProjectView))
}

// ListProjectViews returns a project's saved views, alphabetically so the list renders in a
// stable order regardless of creation order.
func ListProjectViews(ctx context.Context, projectID int64) ([]*ProjectView, error) {
	rows := make([]*ProjectView, 0, 8)
	err := db.GetEngine(ctx).Where("project_id = ?", projectID).OrderBy("name").Find(&rows)
	return rows, err
}

// ProjectViewNameExists reports whether projectID already has a view of that name, compared
// case-insensitively so the result does not depend on the database's collation.
func ProjectViewNameExists(ctx context.Context, projectID int64, name string) (bool, error) {
	return db.GetEngine(ctx).Where("project_id = ? AND LOWER(name) = ?", projectID, strings.ToLower(strings.TrimSpace(name))).Exist(new(ProjectView))
}

// InsertProjectView creates a saved view.
func InsertProjectView(ctx context.Context, row *ProjectView) error {
	return db.Insert(ctx, row)
}

// GetProjectView reads one saved view by id, util.ErrNotExist when there is none.
func GetProjectView(ctx context.Context, id int64) (*ProjectView, error) {
	row := new(ProjectView)
	has, err := db.GetEngine(ctx).ID(id).Get(row)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, util.ErrNotExist
	}
	return row, nil
}

// DeleteProjectView removes a saved view.
func DeleteProjectView(ctx context.Context, id int64) error {
	_, err := db.GetEngine(ctx).ID(id).Delete(new(ProjectView))
	return err
}
