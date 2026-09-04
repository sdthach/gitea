// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"gitea.dev/models/db"
	hub_model "gitea.dev/models/hub"
	"gitea.dev/modules/timeutil"

	"xorm.io/builder"
)

// The audit event set, declared once. The enum is complete here so the deployment matrix projection has
// one closed set to switch on rather than a string it has to guess at.
const (
	AuditRequested = "requested"
	AuditStarted   = "started"
	AuditSucceeded = "succeeded"
	AuditFailed    = "failed"
	AuditCancelled = "cancelled"
	AuditApproved  = "approved"
	AuditRejected  = "rejected"
	// AuditOverridden records that someone who could bypass the sequence rule did so, and
	// why. It is appended only after the deploy it authorized was dispatched, so the
	// row carries the run that is its evidence.
	AuditOverridden = "overridden"
	// AuditChecksPending records that a promotion's pre-deployment checks held it: the
	// deployment row it names is in the waiting state, not dispatched.
	AuditChecksPending = "checks_pending"
	// AuditChecksPassed records that a waiting deployment's checks all passed on
	// re-evaluation and it was dispatched.
	AuditChecksPassed = "checks_passed"
	// AuditChecksFailed records that a waiting deployment's checks turned up a failure on
	// re-evaluation; the deployment moves to the failed state and dispatches nothing.
	AuditChecksFailed = "checks_failed"
	// AuditAutoPromoted records that an environment's auto_promote column, not a person,
	// created this deployment because every environment it depends on held the release live.
	AuditAutoPromoted = "auto_promoted"
)

// AuditEvents is the complete event set, in the order the constants above declare it, with
// each addition appended rather than inserted so an existing row's meaning never moves.
var AuditEvents = []string{
	AuditRequested, AuditStarted, AuditSucceeded,
	AuditFailed, AuditCancelled, AuditApproved, AuditRejected,
	AuditOverridden, AuditChecksPending, AuditChecksPassed, AuditChecksFailed, AuditAutoPromoted,
}

// The source set, declared once.
const (
	SourceUI        = "ui"
	SourceNotifier  = "notifier"
	SourceReconcile = "reconcile"
)

// AuditSources is the complete source set.
var AuditSources = []string{SourceUI, SourceNotifier, SourceReconcile}

// AuditEvent is one row of the append-only audit log. It is retained indefinitely; no
// purge or archive path is built.
//
// ActorLogin is denormalized on purpose: deleting the user from Gitea must not erase who
// deployed.
type AuditEvent struct {
	ID           int64              `xorm:"pk autoincr" json:"id"`
	Event        string             `xorm:"VARCHAR(32) INDEX NOT NULL" json:"event"`
	OccurredUnix timeutil.TimeStamp `xorm:"INDEX NOT NULL" json:"occurred_unix"`
	ActorID      int64              `xorm:"INDEX NOT NULL DEFAULT 0" json:"actor_id"`
	ActorLogin   string             `xorm:"VARCHAR(255) NOT NULL DEFAULT ''" json:"actor_login"`
	RepoID       int64              `xorm:"INDEX NOT NULL" json:"repo_id"`
	Environment  string             `xorm:"VARCHAR(64) INDEX NOT NULL" json:"environment"`
	ReleaseTag   string             `xorm:"VARCHAR(255) INDEX NOT NULL DEFAULT ''" json:"release_tag"`
	SHA          string             `xorm:"VARCHAR(64) NOT NULL DEFAULT ''" json:"sha"`
	Branch       string             `xorm:"VARCHAR(255) NOT NULL DEFAULT ''" json:"branch"`
	RunID        int64              `xorm:"INDEX NOT NULL DEFAULT 0" json:"run_id"`
	RunURL       string             `xorm:"VARCHAR(255) NOT NULL DEFAULT ''" json:"run_url"`
	Source       string             `xorm:"VARCHAR(32) NOT NULL" json:"source"`
	// Reason is the actor's own words for an event that needed them: the reason given when
	// the sequence rule was overridden. It is empty on every event that needs none.
	Reason      string             `xorm:"TEXT NOT NULL DEFAULT ''" json:"reason"`
	CreatedUnix timeutil.TimeStamp `xorm:"created NOT NULL" json:"created_unix"`
}

// TableName keeps every fork table under one prefix.
func (*AuditEvent) TableName() string { return "deploy_audit" }

func init() {
	db.RegisterModel(new(AuditEvent))
}

// errAppendOnly is the rejection both append-only tables share. It is what an attempt to
// rewrite an existing row gets: the row already stands, and the log is the record.
func errAppendOnly(table string, id int64) error {
	return &hub_model.Error{
		Message: fmt.Sprintf("%s row %d already exists and %s is append-only", table, id, table),
		SuggestedAction: "Append a new row describing what changed instead of rewriting this one. " +
			"The API exposes no POST, PATCH or DELETE on the audit resource.",
	}
}

// ValidateAuditEvent refuses a row outside the declared enums. An unrecognised event or
// source would render as an unknown cell state rather than failing where it was written.
func ValidateAuditEvent(e *AuditEvent) error {
	if !slices.Contains(AuditEvents, e.Event) {
		return &hub_model.Error{
			Message:         fmt.Sprintf("%q is not a deployments audit event", e.Event),
			SuggestedAction: "Use one of: " + strings.Join(AuditEvents, ", ") + ".",
		}
	}
	if !slices.Contains(AuditSources, e.Source) {
		return &hub_model.Error{
			Message:         fmt.Sprintf("%q is not a deployments audit source", e.Source),
			SuggestedAction: "Use one of: " + strings.Join(AuditSources, ", ") + ".",
		}
	}
	if e.RepoID <= 0 {
		return &hub_model.Error{
			Message:         fmt.Sprintf("audit event repo_id is %d", e.RepoID),
			SuggestedAction: "Record the event against the repository the run belongs to.",
		}
	}
	if NormalizeEnvironmentName(e.Environment) == "" {
		return &hub_model.Error{
			Message:         "audit event names no environment",
			SuggestedAction: "Name the workflow file deploy-<env>.yaml so the environment is read from it, or set environment explicitly.",
		}
	}
	// An override is the reason it records. A row saying only that the sequence rule was
	// bypassed answers none of what the log is kept for, so the write path refuses it.
	if e.Event == AuditOverridden && strings.TrimSpace(e.Reason) == "" {
		return &hub_model.Error{
			Message:         "an overridden event carries no reason",
			SuggestedAction: "Send override_reason with the deploy; the sequence rule is only bypassable with a reason on the record.",
		}
	}
	return nil
}

// AppendAuditEvent appends one event. It is the only write path to the table: there is no
// update and no delete, so the log can only grow.
//
// A row carrying a primary key is what an update looks like when written through the model,
// and it is refused rather than silently inserted as a duplicate.
func AppendAuditEvent(ctx context.Context, e *AuditEvent) error {
	if e.ID != 0 {
		return errAppendOnly("deploy_audit", e.ID)
	}
	e.Environment = NormalizeEnvironmentName(e.Environment)
	if err := ValidateAuditEvent(e); err != nil {
		return err
	}
	if e.OccurredUnix == 0 {
		e.OccurredUnix = timeutil.TimeStampNow()
	}
	return db.Insert(ctx, e)
}

// FindAuditEvents lists events matching cond. Like deployments it takes no offset: the
// resource is append-only and pages by cursor.
func FindAuditEvents(ctx context.Context, cond builder.Cond, orderBy string, limit int) ([]*AuditEvent, error) {
	sess := db.GetEngine(ctx).Where(cond).OrderBy(orderBy)
	if limit > 0 {
		sess = sess.Limit(limit)
	}
	rows := make([]*AuditEvent, 0, 16)
	if err := sess.Find(&rows); err != nil {
		return nil, err
	}
	return rows, nil
}
