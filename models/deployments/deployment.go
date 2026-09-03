// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"context"
	"fmt"

	"gitea.dev/models/db"
	hub_model "gitea.dev/models/hub"
	"gitea.dev/modules/timeutil"

	"xorm.io/builder"
)

// Deployment is one deployment of a release to an environment.
//
// The table is APPEND-ONLY. It is never upserted per (release_tag, environment): deploying
// v1 to qa, then v2, then v1 again leaves three rows, and without all three the grid cannot
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
