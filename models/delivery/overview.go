// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"

	actions_model "gitea.dev/models/actions"
	"gitea.dev/models/db"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"

	"xorm.io/builder"
)

// The CI overview adds NO table. Every figure it shows is read from Gitea's own action_run
// and from the fork's delivery_deployment, so there is nothing to keep in step with either.
//
// Aggregates are computed in process over reduced rows rather than in SQL. Grouping runs by
// UTC day is the reason: SQLite spells it strftime and PostgreSQL spells it date_trunc, and
// one schema has to answer both. Reducing each row to six columns first is what keeps
// that affordable.

// MaxOverviewRunFacts caps how many runs one aggregate reads. A window wide enough to
// exceed it returns a truncated aggregate that says so, rather than a wrong number that
// does not (see Truncated on the service's Overview).
const MaxOverviewRunFacts = 20000

// RunFact is one Actions run reduced to what an aggregate depends on. Taking a reduced
// struct rather than the model keeps the projections pure and testable with no database.
type RunFact struct {
	ID          int64
	RepoID      int64
	WorkflowID  string
	Status      int
	StartedUnix int64
	StoppedUnix int64
	CreatedUnix int64
}

// DurationSeconds is how long the run took. An unfinished run, and a run whose stop
// timestamp precedes its start, both contribute zero rather than a negative duration that
// would silently reduce a total.
func (f RunFact) DurationSeconds() int64 {
	if f.StartedUnix <= 0 || f.StoppedUnix <= f.StartedUnix {
		return 0
	}
	return f.StoppedUnix - f.StartedUnix
}

// DeploymentFact is one deployment reduced to what the daily trend needs.
type DeploymentFact struct {
	ID          int64
	RepoID      int64
	CreatedUnix int64
}

// FindRunFacts reads the runs of repoIDs created in [fromUnix, toUnix).
//
// It is fail-closed on an empty repository set: an aggregate over "no repositories" is an
// empty aggregate, never an unscoped one. repoIDs is always the caller's own accessible set,
// resolved by Gitea's existing permission filtering before it reaches here.
func FindRunFacts(ctx context.Context, repoIDs []int64, fromUnix, toUnix int64) ([]RunFact, bool, error) {
	if len(repoIDs) == 0 {
		return []RunFact{}, false, nil
	}
	cond := builder.In("repo_id", repoIDs).
		And(builder.Gte{"created": fromUnix}).
		And(builder.Lt{"created": toUnix})

	runs := make([]*actions_model.ActionRun, 0, 64)
	err := db.GetEngine(ctx).
		Where(cond).
		Cols("id", "repo_id", "workflow_id", "status", "started", "stopped", "created").
		OrderBy("created DESC, id DESC").
		Limit(MaxOverviewRunFacts + 1).
		Find(&runs)
	if err != nil {
		return nil, false, err
	}
	truncated := len(runs) > MaxOverviewRunFacts
	if truncated {
		runs = runs[:MaxOverviewRunFacts]
	}
	facts := make([]RunFact, 0, len(runs))
	for _, r := range runs {
		facts = append(facts, RunFact{
			ID:          r.ID,
			RepoID:      r.RepoID,
			WorkflowID:  r.WorkflowID,
			Status:      int(r.Status),
			StartedUnix: int64(r.Started),
			StoppedUnix: int64(r.Stopped),
			CreatedUnix: int64(r.Created),
		})
	}
	return facts, truncated, nil
}

// FindDeploymentFacts reads the deployments of repoIDs appended in [fromUnix, toUnix). The
// daily trend's deployment count reads this table rather than counting deploy runs, so the
// CI overview and the delivery grid share one source of truth.
func FindDeploymentFacts(ctx context.Context, repoIDs []int64, fromUnix, toUnix int64) ([]DeploymentFact, error) {
	if len(repoIDs) == 0 {
		return []DeploymentFact{}, nil
	}
	cond := builder.In("repo_id", repoIDs).
		And(builder.Gte{"created_unix": fromUnix}).
		And(builder.Lt{"created_unix": toUnix})

	rows, err := FindDeployments(ctx, cond, "created_unix DESC, id DESC", MaxOverviewRunFacts)
	if err != nil {
		return nil, err
	}
	facts := make([]DeploymentFact, 0, len(rows))
	for _, d := range rows {
		facts = append(facts, DeploymentFact{ID: d.ID, RepoID: d.RepoID, CreatedUnix: int64(d.CreatedUnix)})
	}
	return facts, nil
}

// FindDisabledWorkflows reads the workflow files disabled per repository, out of Gitea's own
// Actions unit configuration — the same list its repository settings page writes. Nothing is
// mirrored, so a workflow disabled outside this feature is counted immediately.
func FindDisabledWorkflows(ctx context.Context, repoIDs []int64) (map[int64][]string, error) {
	out := map[int64][]string{}
	if len(repoIDs) == 0 {
		return out, nil
	}
	units := make([]*repo_model.RepoUnit, 0, len(repoIDs))
	err := db.GetEngine(ctx).
		Where(builder.In("repo_id", repoIDs).And(builder.Eq{"type": unit.TypeActions})).
		Find(&units)
	if err != nil {
		return nil, err
	}
	for _, u := range units {
		cfg := u.ActionsConfig()
		if cfg == nil || len(cfg.DisabledWorkflows) == 0 {
			continue
		}
		out[u.RepoID] = append(out[u.RepoID], cfg.DisabledWorkflows...)
	}
	return out, nil
}

// FindRuns lists runs matching cond, for the cross-repository run list Gitea has no
// endpoint for. It takes the condition the query grammar rendered, so filtering,
// sorting and paging are the grammar's, not a second implementation of it.
func FindRuns(ctx context.Context, cond builder.Cond, orderBy string, limit, offset int) ([]*actions_model.ActionRun, int64, error) {
	sess := db.GetEngine(ctx).Where(cond).OrderBy(orderBy)
	if limit > 0 {
		sess = sess.Limit(limit, offset)
	}
	runs := make([]*actions_model.ActionRun, 0, 16)
	total, err := sess.FindAndCount(&runs)
	if err != nil {
		return nil, 0, err
	}
	return runs, total, nil
}
