// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package planning holds the fork's roadmap schedule rows. It imports nothing from
// models/hub, which imports this package to convert old marker comments into these rows.
package planning

import (
	"context"
	"regexp"
	"strings"
	"time"

	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"
)

// IssueSchedule is the recorded start date of one issue.
type IssueSchedule struct {
	ID          int64              `xorm:"pk autoincr"`
	IssueID     int64              `xorm:"UNIQUE NOT NULL"`
	StartUnix   timeutil.TimeStamp `xorm:"NOT NULL"`
	CreatedUnix timeutil.TimeStamp `xorm:"created NOT NULL"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated NOT NULL"`
}

func (*IssueSchedule) TableName() string { return "plan_issue_schedule" }

// MilestoneSchedule is the recorded start date of one milestone.
type MilestoneSchedule struct {
	ID          int64              `xorm:"pk autoincr"`
	MilestoneID int64              `xorm:"UNIQUE NOT NULL"`
	StartUnix   timeutil.TimeStamp `xorm:"NOT NULL"`
	CreatedUnix timeutil.TimeStamp `xorm:"created NOT NULL"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated NOT NULL"`
}

func (*MilestoneSchedule) TableName() string { return "plan_milestone_schedule" }

func init() {
	db.RegisterModel(new(IssueSchedule))
	db.RegisterModel(new(MilestoneSchedule))
}

// UpsertIssueStart records issueID's start, replacing any previous value.
func UpsertIssueStart(ctx context.Context, issueID, start int64) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		row := new(IssueSchedule)
		has, err := db.GetEngine(ctx).Where("issue_id = ?", issueID).Get(row)
		if err != nil {
			return err
		}
		if has {
			row.StartUnix = timeutil.TimeStamp(start)
			_, err = db.GetEngine(ctx).ID(row.ID).Cols("start_unix").Update(row)
			return err
		}
		return db.Insert(ctx, &IssueSchedule{IssueID: issueID, StartUnix: timeutil.TimeStamp(start)})
	})
}

// DeleteIssueStart removes issueID's recorded start, if any.
func DeleteIssueStart(ctx context.Context, issueID int64) error {
	_, err := db.GetEngine(ctx).Where("issue_id = ?", issueID).Delete(new(IssueSchedule))
	return err
}

// IssueStarts reads every recorded start among issueIDs. An id with no row is simply absent
// from the result, which every caller reads as "no recorded start".
func IssueStarts(ctx context.Context, issueIDs []int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	if len(issueIDs) == 0 {
		return out, nil
	}
	rows := make([]*IssueSchedule, 0, len(issueIDs))
	if err := db.GetEngine(ctx).In("issue_id", issueIDs).Find(&rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.IssueID] = int64(row.StartUnix)
	}
	return out, nil
}

// UpsertMilestoneStart records milestoneID's start, replacing any previous value.
func UpsertMilestoneStart(ctx context.Context, milestoneID, start int64) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		row := new(MilestoneSchedule)
		has, err := db.GetEngine(ctx).Where("milestone_id = ?", milestoneID).Get(row)
		if err != nil {
			return err
		}
		if has {
			row.StartUnix = timeutil.TimeStamp(start)
			_, err = db.GetEngine(ctx).ID(row.ID).Cols("start_unix").Update(row)
			return err
		}
		return db.Insert(ctx, &MilestoneSchedule{MilestoneID: milestoneID, StartUnix: timeutil.TimeStamp(start)})
	})
}

// DeleteMilestoneStart removes milestoneID's recorded start, if any.
func DeleteMilestoneStart(ctx context.Context, milestoneID int64) error {
	_, err := db.GetEngine(ctx).Where("milestone_id = ?", milestoneID).Delete(new(MilestoneSchedule))
	return err
}

// MilestoneStarts reads every recorded start among milestoneIDs.
func MilestoneStarts(ctx context.Context, milestoneIDs []int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	if len(milestoneIDs) == 0 {
		return out, nil
	}
	rows := make([]*MilestoneSchedule, 0, len(milestoneIDs))
	if err := db.GetEngine(ctx).In("milestone_id", milestoneIDs).Find(&rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.MilestoneID] = int64(row.StartUnix)
	}
	return out, nil
}

// StartedMarker is the trailer issue-sync used to post on a progress comment, back when a
// start had nowhere else to live. Migration 4 reads it off every existing comment; nothing
// writes it any more.
var StartedMarker = regexp.MustCompile(`ccpm:started=([0-9TZ:+\-]{4,40})`)

// ParseStartedMarker reads a ccpm:started marker out of a comment body and parses it as
// RFC 3339, ok=false when the comment carries none or the value does not parse.
func ParseStartedMarker(body string) (int64, bool) {
	m := StartedMarker.FindStringSubmatch(body)
	if m == nil {
		return 0, false
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(m[1]))
	if err != nil {
		return 0, false
	}
	return at.Unix(), true
}
