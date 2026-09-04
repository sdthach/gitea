// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"context"
	"time"
)

// runSweeper calls sweep every interval until ctx is done, so a caller running it in a
// goroutine can rely on it to exit with the process rather than leak past shutdown.
func runSweeper(ctx context.Context, interval time.Duration, sweep func()) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}
