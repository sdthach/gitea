// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"context"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
)

// RunningStopwatches reads every stopwatch running on one of repoID's own issues, joining
// through the issue table rather than materialising the repository's issue ids first.
func RunningStopwatches(ctx context.Context, repoID int64) ([]*issues_model.Stopwatch, error) {
	var sws []*issues_model.Stopwatch
	err := db.GetEngine(ctx).
		Table("stopwatch").
		Join("INNER", "issue", "issue.id = stopwatch.issue_id").
		Where("issue.repo_id = ?", repoID).
		Find(&sws)
	return sws, err
}
