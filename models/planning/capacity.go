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

// UserCapacity is one user's declared capacity, scoped to a repository, an organization or
// the whole instance (RepoID 0 and OrgID 0), visible everywhere nothing narrower shadows it.
type UserCapacity struct {
	ID          int64   `xorm:"pk autoincr"`
	UserID      int64   `xorm:"UNIQUE(scope_user) INDEX NOT NULL"`
	RepoID      int64   `xorm:"UNIQUE(scope_user) INDEX NOT NULL DEFAULT 0"`
	OrgID       int64   `xorm:"UNIQUE(scope_user) INDEX NOT NULL DEFAULT 0"`
	HoursPerDay float64 `xorm:"NOT NULL DEFAULT 8"`
	Utilization float64 `xorm:"NOT NULL DEFAULT 0.8"`
	// Workdays is a bit mask over the week, Sunday = bit 0, matching time.Weekday directly.
	// 62 (0b0111110) is Monday through Friday.
	Workdays    int                `xorm:"NOT NULL DEFAULT 62"`
	CreatedUnix timeutil.TimeStamp `xorm:"created NOT NULL"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated NOT NULL"`
}

func (*UserCapacity) TableName() string { return "plan_user_capacity" }

func init() {
	db.RegisterModel(new(UserCapacity))
}

// GetUserCapacity reads one user's row in scope, util.ErrNotExist when there is none.
func GetUserCapacity(ctx context.Context, userID, repoID, orgID int64) (*UserCapacity, error) {
	row := new(UserCapacity)
	has, err := db.GetEngine(ctx).Where("user_id = ? AND repo_id = ? AND org_id = ?", userID, repoID, orgID).Get(row)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, util.ErrNotExist
	}
	return row, nil
}

// UpsertUserCapacity records row's capacity, replacing any previous row for the same
// (user_id, repo_id, org_id).
func UpsertUserCapacity(ctx context.Context, row *UserCapacity) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		existing := new(UserCapacity)
		has, err := db.GetEngine(ctx).Where("user_id = ? AND repo_id = ? AND org_id = ?", row.UserID, row.RepoID, row.OrgID).Get(existing)
		if err != nil {
			return err
		}
		if has {
			existing.HoursPerDay, existing.Utilization, existing.Workdays = row.HoursPerDay, row.Utilization, row.Workdays
			if _, err := db.GetEngine(ctx).ID(existing.ID).Cols("hours_per_day", "utilization", "workdays").Update(existing); err != nil {
				return err
			}
			row.ID = existing.ID
			return nil
		}
		return db.Insert(ctx, row)
	})
}

// DeleteUserCapacity removes one user's row in scope, if any.
func DeleteUserCapacity(ctx context.Context, userID, repoID, orgID int64) error {
	_, err := db.GetEngine(ctx).Where("user_id = ? AND repo_id = ? AND org_id = ?", userID, repoID, orgID).Delete(new(UserCapacity))
	return err
}

// CapacitiesFor reads every row among userIDs across the three scopes a resolution walks —
// repoID's own, orgID's own, and the instance's — in one query. Nearest-scope shadowing per
// user is the caller's concern.
func CapacitiesFor(ctx context.Context, userIDs []int64, repoID, orgID int64) ([]*UserCapacity, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	var cond builder.Cond = builder.Eq{"repo_id": 0, "org_id": 0}
	if orgID > 0 {
		cond = builder.Or(cond, builder.Eq{"repo_id": 0, "org_id": orgID})
	}
	if repoID > 0 {
		cond = builder.Or(cond, builder.Eq{"repo_id": repoID, "org_id": 0})
	}
	rows := make([]*UserCapacity, 0, len(userIDs))
	err := db.GetEngine(ctx).Where(cond).In("user_id", userIDs).Find(&rows)
	return rows, err
}

// IssueUserTime is one issue's tracked seconds by one user, deleted entries excluded.
type IssueUserTime struct {
	IssueID int64
	UserID  int64
	Time    int64
}

// TrackedByIssueUser reads every user's tracked seconds among issueIDs in one grouped query
// over tracked_time.
func TrackedByIssueUser(ctx context.Context, issueIDs []int64) (map[[2]int64]int64, error) {
	out := map[[2]int64]int64{}
	if len(issueIDs) == 0 {
		return out, nil
	}
	rows := make([]*IssueUserTime, 0, len(issueIDs))
	err := db.GetEngine(ctx).Table("tracked_time").
		Select("issue_id, user_id, sum(time) AS time").
		Where("deleted = ?", false).
		In("issue_id", issueIDs).
		GroupBy("issue_id, user_id").
		Find(&rows)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[[2]int64{row.IssueID, row.UserID}] = row.Time
	}
	return out, nil
}
