// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package approvalgate is the seam models/actions/task.go delegates job assignment through.
//
// It is deliberately a LEAF: it imports nothing of Gitea's. models/deployments imports
// models/actions (secret.go and jobenv.go both take an *actions_model.ActionRunJob), so a
// gate function living in models/deployments could not be called from models/actions without
// an import cycle. Measured in this tree: `go list -f '{{.Imports}}' ./models/deployments`
// lists gitea.dev/models/actions. This package is what the spoke names instead, and
// models/deployments registers the real gate into it from its own init().
package approvalgate

import (
	"context"
	"sync/atomic"
)

// HeldFunc reports whether the job identified by (repoID, jobID) is waiting on an approval
// and must therefore not be handed to a runner. It answers true when it cannot tell: an
// unassigned job is recoverable, a production deploy that ran without its approval is not.
type HeldFunc func(ctx context.Context, repoID, jobID int64) bool

// registered holds the installed gate. It is atomic because Held is called from every
// runner's poll goroutine while a test may install its own gate.
var registered atomic.Pointer[HeldFunc]

// Register installs the gate. models/deployments calls it from its own init(), so the gate is
// live before any runner can poll. Passing nil removes it, which is what a test restoring
// the stock behaviour does.
func Register(f HeldFunc) {
	if f == nil {
		registered.Store(nil)
		return
	}
	registered.Store(&f)
}

// Held is the question CreateTaskForRunner asks of every candidate job.
//
// With NO gate registered it answers false. That is stock Gitea with the fork absent, which
// must claim jobs exactly as it does today. With a gate registered, failing closed
// is the gate's own responsibility, not this dispatcher's.
func Held(ctx context.Context, repoID, jobID int64) bool {
	f := registered.Load()
	if f == nil {
		return false
	}
	return (*f)(ctx, repoID, jobID)
}
