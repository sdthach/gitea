// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"

	"xorm.io/builder"
)

// The audit event set, declared once (E5, E13). Slice 6 gives approved and rejected a
// producer; the enum is complete here so the grid projection has one closed set to switch
// on rather than a string it has to guess at.
const (
	AuditRequested = "requested"
	AuditStarted   = "started"
	AuditSucceeded = "succeeded"
	AuditFailed    = "failed"
	AuditCancelled = "cancelled"
	AuditApproved  = "approved"
	AuditRejected  = "rejected"
)

// AuditEvents is the complete event set, in the order E5 lists it.
var AuditEvents = []string{
	AuditRequested, AuditStarted, AuditSucceeded,
	AuditFailed, AuditCancelled, AuditApproved, AuditRejected,
}

// The source set, declared once (E5).
const (
	SourceUI        = "ui"
	SourceNotifier  = "notifier"
	SourceReconcile = "reconcile"
)

// AuditSources is the complete source set.
var AuditSources = []string{SourceUI, SourceNotifier, SourceReconcile}

// AuditEvent is one row of the append-only audit log (E5). It is retained indefinitely; no
// purge or archive path is built (E13).
//
// ActorLogin is denormalized on purpose: deleting the user from Gitea must not erase who
// deployed (SC 19).
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
	CreatedUnix  timeutil.TimeStamp `xorm:"created NOT NULL" json:"created_unix"`
}

// TableName keeps every fork table under one prefix.
func (*AuditEvent) TableName() string { return "delivery_audit" }

func init() {
	db.RegisterModel(new(AuditEvent))
}

// errAppendOnly is the rejection both append-only tables share. It is what an attempt to
// rewrite an existing row gets: the row already stands, and the log is the record (E5, I11).
func errAppendOnly(table string, id int64) error {
	return &Error{
		Message: fmt.Sprintf("%s row %d already exists and %s is append-only", table, id, table),
		SuggestedAction: "Append a new row describing what changed instead of rewriting this one. " +
			"The API exposes no POST, PATCH or DELETE on the audit resource (I11).",
	}
}

// ValidateAuditEvent refuses a row outside the declared enums. An unrecognised event or
// source would render as an unknown cell state rather than failing where it was written.
func ValidateAuditEvent(e *AuditEvent) error {
	if !slices.Contains(AuditEvents, e.Event) {
		return &Error{
			Message:         fmt.Sprintf("%q is not a delivery audit event", e.Event),
			SuggestedAction: "Use one of: " + strings.Join(AuditEvents, ", ") + ".",
		}
	}
	if !slices.Contains(AuditSources, e.Source) {
		return &Error{
			Message:         fmt.Sprintf("%q is not a delivery audit source", e.Source),
			SuggestedAction: "Use one of: " + strings.Join(AuditSources, ", ") + ".",
		}
	}
	if e.RepoID <= 0 {
		return &Error{
			Message:         fmt.Sprintf("audit event repo_id is %d", e.RepoID),
			SuggestedAction: "Record the event against the repository the run belongs to.",
		}
	}
	if NormalizeEnvironmentName(e.Environment) == "" {
		return &Error{
			Message:         "audit event names no environment",
			SuggestedAction: "Name the workflow file deploy-<env>.yaml so the environment is read from it (D4), or set environment explicitly.",
		}
	}
	return nil
}

// AppendAuditEvent appends one event. It is the only write path to the table: there is no
// update and no delete, so the log can only grow (E5, E13).
//
// A row carrying a primary key is what an update looks like when written through the model,
// and it is refused rather than silently inserted as a duplicate.
func AppendAuditEvent(ctx context.Context, e *AuditEvent) error {
	if e.ID != 0 {
		return errAppendOnly("delivery_audit", e.ID)
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
// resource is append-only and pages by cursor (I6).
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
