// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunSweeper(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		runSweeper(ctx, time.Millisecond, func() { calls.Add(1) })
		close(done)
	}()

	require.Eventually(t, func() bool { return calls.Load() > 0 }, time.Second, time.Millisecond,
		"the sweep function runs at least once")

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runSweeper did not return after ctx was cancelled")
	}
	assert.Positive(t, calls.Load())
}
