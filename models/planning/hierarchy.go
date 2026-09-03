// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"context"

	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"
)

// IssueParent is one child's recorded parent, the row hierarchy uses in place of the retired
// label convention. ChildIssueID is unique: an issue has at most one parent.
type IssueParent struct {
	ID            int64              `xorm:"pk autoincr"`
	ChildIssueID  int64              `xorm:"UNIQUE NOT NULL"`
	ParentIssueID int64              `xorm:"INDEX NOT NULL"`
	CreatedUnix   timeutil.TimeStamp `xorm:"created NOT NULL"`
}

func (*IssueParent) TableName() string { return "plan_issue_parent" }

func init() {
	db.RegisterModel(new(IssueParent))
}

// UpsertParent records childID's parent, replacing any previous value.
func UpsertParent(ctx context.Context, childID, parentID int64) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		row := new(IssueParent)
		has, err := db.GetEngine(ctx).Where("child_issue_id = ?", childID).Get(row)
		if err != nil {
			return err
		}
		if has {
			row.ParentIssueID = parentID
			_, err = db.GetEngine(ctx).ID(row.ID).Cols("parent_issue_id").Update(row)
			return err
		}
		return db.Insert(ctx, &IssueParent{ChildIssueID: childID, ParentIssueID: parentID})
	})
}

// DeleteParent removes childID's recorded parent, if any.
func DeleteParent(ctx context.Context, childID int64) error {
	_, err := db.GetEngine(ctx).Where("child_issue_id = ?", childID).Delete(new(IssueParent))
	return err
}

// ParentMapForRepo reads every parent edge among repoID's own issues, in one query joined on
// issue.repo_id, child -> parent.
func ParentMapForRepo(ctx context.Context, repoID int64) (map[int64]int64, error) {
	rows := make([]*IssueParent, 0, 32)
	err := db.GetEngine(ctx).Table("plan_issue_parent").
		Join("INNER", "issue", "issue.id = plan_issue_parent.child_issue_id").
		Where("issue.repo_id = ?", repoID).
		Find(&rows)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int64, len(rows))
	for _, row := range rows {
		out[row.ChildIssueID] = row.ParentIssueID
	}
	return out, nil
}

// ParentsOf reads the recorded parent of every id in issueIDs. An id with no row is simply
// absent from the result.
func ParentsOf(ctx context.Context, issueIDs []int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	if len(issueIDs) == 0 {
		return out, nil
	}
	rows := make([]*IssueParent, 0, len(issueIDs))
	if err := db.GetEngine(ctx).In("child_issue_id", issueIDs).Find(&rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ChildIssueID] = row.ParentIssueID
	}
	return out, nil
}

// ChildrenOf reads every child recorded under any id in parentIDs, keyed by that parent.
func ChildrenOf(ctx context.Context, parentIDs []int64) (map[int64][]int64, error) {
	out := map[int64][]int64{}
	if len(parentIDs) == 0 {
		return out, nil
	}
	rows := make([]*IssueParent, 0, len(parentIDs))
	if err := db.GetEngine(ctx).In("parent_issue_id", parentIDs).OrderBy("child_issue_id").Find(&rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ParentIssueID] = append(out[row.ParentIssueID], row.ChildIssueID)
	}
	return out, nil
}

// HasLinks reports whether issueID is a child, a parent, or both.
func HasLinks(ctx context.Context, issueID int64) (bool, error) {
	isChild, err := db.GetEngine(ctx).Where("child_issue_id = ?", issueID).Exist(new(IssueParent))
	if err != nil || isChild {
		return isChild, err
	}
	return db.GetEngine(ctx).Where("parent_issue_id = ?", issueID).Exist(new(IssueParent))
}
