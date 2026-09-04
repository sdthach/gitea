// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"context"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"
	"gitea.dev/modules/timeutil"

	"xorm.io/builder"
)

func init() {
	RegisterMigration(&Migration{
		ID:          4,
		Description: "convert ccpm:started marker comments into plan_issue_schedule rows",
		Migrate:     migrateScheduleFromMarkers,
	})
}

// migrateScheduleFromMarkers reads every comment ever posted, oldest first, and keeps the
// last valid marker value per issue — the same rule the roadmap's own reader used to apply.
// A malformed value is skipped rather than overwriting a good one already seen for that
// issue. An issue that already has a schedule row is left alone, so a rerun inserts nothing:
// the migration is the one-time bridge, and a later PUT of the same issue's schedule is the
// row it wrote, not a marker.
func migrateScheduleFromMarkers(ctx context.Context, e db.Engine) error {
	comments := make([]*issues_model.Comment, 0, 16)
	if err := e.Where(builder.Eq{"type": issues_model.CommentTypeComment}.And(builder.Like{"content", "ccpm:started="})).
		OrderBy("issue_id ASC, created_unix ASC, id ASC").
		Find(&comments); err != nil {
		return err
	}

	starts := map[int64]int64{}
	for _, comment := range comments {
		if start, ok := planning_model.ParseStartedMarker(comment.Content); ok {
			starts[comment.IssueID] = start
		}
	}

	for issueID, start := range starts {
		has, err := e.Where("issue_id = ?", issueID).Exist(new(planning_model.IssueSchedule))
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := e.Insert(&planning_model.IssueSchedule{
			IssueID: issueID, StartUnix: timeutil.TimeStamp(start),
		}); err != nil {
			return err
		}
	}
	return nil
}
