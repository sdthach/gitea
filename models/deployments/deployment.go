// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"gitea.dev/models/db"
	hub_model "gitea.dev/models/hub"
	"gitea.dev/modules/timeutil"

	"xorm.io/builder"
)

// Deployment is one deployment of a release to an environment.
//
// The table is APPEND-ONLY. It is never upserted per (release_tag, environment): deploying
// v1 to qa, then v2, then v1 again leaves three rows, and without all three the deployment matrix cannot
// show that a version was deployed somewhere previously.
//
// Version identity belongs to the release. A row points at a release tag and carries no
// version string to be parsed. Every id is Gitea's own; there is no foreign forge id
// to reconcile.
type Deployment struct {
	ID          int64  `xorm:"pk autoincr" json:"id"`
	RepoID      int64  `xorm:"INDEX UNIQUE(run_env) NOT NULL" json:"repo_id"`
	Environment string `xorm:"VARCHAR(64) UNIQUE(run_env) NOT NULL" json:"environment"`
	ReleaseTag  string `xorm:"INDEX VARCHAR(255) NOT NULL" json:"release_tag"`
	SHA         string `xorm:"VARCHAR(64) NOT NULL DEFAULT ''" json:"sha"`
	Branch      string `xorm:"VARCHAR(255) NOT NULL DEFAULT ''" json:"branch"`
	RunID       int64  `xorm:"INDEX UNIQUE(run_env) NOT NULL DEFAULT 0" json:"run_id"`
	RunURL      string `xorm:"VARCHAR(255) NOT NULL DEFAULT ''" json:"run_url"`
	// Status is the run status at the moment the deployment was first recorded. It is
	// written once and never updated: a cell's current state is projected from the
	// append-only audit log, never read from a mutable column.
	Status      string             `xorm:"VARCHAR(32) NOT NULL" json:"status"`
	CreatedUnix timeutil.TimeStamp `xorm:"created INDEX NOT NULL" json:"created_unix"`
}

// TableName keeps every fork table under one prefix, so no fork table can collide with an
// upstream one a later pin introduces.
func (*Deployment) TableName() string { return "deploy_deployment" }

func init() {
	db.RegisterModel(new(Deployment))
}

// The deployment status set a placeholder row — one appended before any Actions run exists,
// while its checks are pending — can hold. Every other value in the column comes verbatim
// from an actions_model.Status.String(), including "waiting", which a real run also reports
// while it queues on concurrency; a placeholder is told apart by RunID being negative.
const (
	DeploymentStatusWaiting = "waiting"
	DeploymentStatusFailed  = "failed"
)

// placeholderRunSeq hands out the negative RunID a checks-pending deployment is appended
// under, before any Actions run exists to name. It starts below any real run id (which is
// always positive) and counts down for the life of the process, so two placeholders appended
// in the same nanosecond still land on distinct rows; a collision after a restart would only
// ever affect two placeholders racing for the exact same (repo, environment) pair, which the
// unique index then refuses as it would any other duplicate.
var placeholderRunSeq = -time.Now().UnixNano()

// nextPlaceholderRunID returns a RunID no real Actions run will ever hold.
func nextPlaceholderRunID() int64 { return atomic.AddInt64(&placeholderRunSeq, -1) }

// AppendPlaceholderDeployment appends one not-yet-dispatched deployment: a promotion whose
// checks are still pending. It is the one caller of AppendDeployment that supplies its own
// RunID, because there is no run yet for the notifier to have assigned one from.
func AppendPlaceholderDeployment(ctx context.Context, d *Deployment) error {
	d.RunID = nextPlaceholderRunID()
	d.Status = DeploymentStatusWaiting
	return AppendDeployment(ctx, d)
}

// DeletePlaceholderDeployment removes a checks-pending row once its checks have all passed
// and the real run has been dispatched: the notifier appends the row that is this deploy's
// actual history once Actions reports the run, so the placeholder is deleted rather than kept
// as a second, stale entry for the same intention.
//
// It is scoped to RunID <= 0 so it can never reach a row that names a real Actions run — the
// one exception to the table being append-only is a row that was never a dispatched run to
// begin with.
func DeletePlaceholderDeployment(ctx context.Context, id int64) error {
	_, err := db.GetEngine(ctx).Where("id = ? AND run_id <= 0", id).Delete(new(Deployment))
	return err
}

// FailPlaceholderDeployment marks a checks-pending row failed once re-evaluation finds a
// check that will not pass on its own — a prerelease reaching a releases-only environment, or
// the dispatch attempt itself failing once checks did pass. Scoped to RunID <= 0 for the same
// reason DeletePlaceholderDeployment is.
func FailPlaceholderDeployment(ctx context.Context, id int64) error {
	_, err := db.GetEngine(ctx).Where("id = ? AND run_id <= 0", id).Cols("status").Update(&Deployment{Status: DeploymentStatusFailed})
	return err
}

// ActiveDeploymentExists reports whether repo/environment already has a deployment in the
// waiting or the running state — the two exclusive_lock treats as busy. Any other status is
// terminal and does not hold the lock.
func ActiveDeploymentExists(ctx context.Context, repoID int64, environment string) (bool, error) {
	return db.GetEngine(ctx).
		Where("repo_id = ? AND environment = ? AND status IN (?, ?)",
			repoID, NormalizeEnvironmentName(environment), DeploymentStatusWaiting, "running").
		Exist(new(Deployment))
}

// DeploymentExists reports whether repo/environment already has any deployment row — waiting,
// dispatched or finished — of releaseTag. AutoPromote reads it to promote a release into a
// dependent environment at most once.
func DeploymentExists(ctx context.Context, repoID int64, environment, releaseTag string) (bool, error) {
	return db.GetEngine(ctx).
		Where("repo_id = ? AND environment = ? AND release_tag = ?", repoID, NormalizeEnvironmentName(environment), releaseTag).
		Exist(new(Deployment))
}

// ValidateDeployment refuses a row the API or the notifier would otherwise persist. Every
// message carries a suggested next action.
func ValidateDeployment(d *Deployment) error {
	if d.RepoID <= 0 {
		return &hub_model.Error{
			Message:         fmt.Sprintf("deployment repo_id is %d", d.RepoID),
			SuggestedAction: "Record the deployment against the repository the run belongs to.",
		}
	}
	if NormalizeEnvironmentName(d.Environment) == "" {
		return &hub_model.Error{
			Message:         "deployment names no environment",
			SuggestedAction: "Name the workflow file deploy-<env>.yaml so the environment is read from it, or set environment explicitly.",
		}
	}
	if d.ReleaseTag == "" {
		return &hub_model.Error{
			Message:         "deployment names no release tag",
			SuggestedAction: "Dispatch the deploy workflow with the release tag as Ref; a deployment points at a release, never at a version string.",
		}
	}
	return nil
}

// AppendDeployment appends one deployment row.
//
// It is the only write path to the table, and it is an append: a row carrying a primary key
// is what an update looks like when written through the model, and it is refused. A second
// call for a (repo, environment, run) already recorded is a no-op rather than an overwrite,
// so a run that reports several status changes still leaves exactly one deployment row.
func AppendDeployment(ctx context.Context, d *Deployment) error {
	if d.ID != 0 {
		return errAppendOnly("deploy_deployment", d.ID)
	}
	d.Environment = NormalizeEnvironmentName(d.Environment)
	if err := ValidateDeployment(d); err != nil {
		return err
	}
	exists, err := db.GetEngine(ctx).
		Where("repo_id = ? AND environment = ? AND run_id = ?", d.RepoID, d.Environment, d.RunID).
		Exist(new(Deployment))
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return db.Insert(ctx, d)
}

// GetDeploymentByID reads one deployment by primary key.
func GetDeploymentByID(ctx context.Context, id int64) (*Deployment, error) {
	d := new(Deployment)
	has, err := db.GetEngine(ctx).ID(id).Get(d)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, &hub_model.Error{
			Message:         fmt.Sprintf("no deployment with id %d", id),
			SuggestedAction: "List /api/deployments/v1/deployments to see the deployments that exist.",
		}
	}
	return d, nil
}

// FindDeployments lists deployments matching cond. It takes no offset: the resource is
// append-only and pages by cursor, because an offset traversal over a table receiving
// concurrent inserts returns rows twice and misses others.
func FindDeployments(ctx context.Context, cond builder.Cond, orderBy string, limit int) ([]*Deployment, error) {
	sess := db.GetEngine(ctx).Where(cond).OrderBy(orderBy)
	if limit > 0 {
		sess = sess.Limit(limit)
	}
	rows := make([]*Deployment, 0, 16)
	if err := sess.Find(&rows); err != nil {
		return nil, err
	}
	return rows, nil
}
