// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package approvalgate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDeliveryApprovalGateDefaultsOpen is SC 21's fork-absent case: nothing registered means
// the dispatcher claims jobs exactly as stock Gitea does. Any other default would hold every
// job in an instance that does not run the fork's hub.
func TestDeliveryApprovalGateDefaultsOpen(t *testing.T) {
	previous := registered.Load()
	t.Cleanup(func() { registered.Store(previous) })

	Register(nil)
	assert.False(t, Held(t.Context(), 7, 5))
}

// TestDeliveryApprovalGateDelegates proves the dispatcher passes the job through and returns
// what the gate says, in both directions. A dispatcher that dropped the answer would be a
// gate that never held anything.
func TestDeliveryApprovalGateDelegates(t *testing.T) {
	previous := registered.Load()
	t.Cleanup(func() { registered.Store(previous) })

	var sawRepo, sawJob int64
	Register(func(_ context.Context, repoID, jobID int64) bool {
		sawRepo, sawJob = repoID, jobID
		return true
	})
	assert.True(t, Held(t.Context(), 7, 5))
	assert.Equal(t, int64(7), sawRepo)
	assert.Equal(t, int64(5), sawJob)

	Register(func(context.Context, int64, int64) bool { return false })
	assert.False(t, Held(t.Context(), 7, 5))
}
