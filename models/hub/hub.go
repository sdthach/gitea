// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package hub holds the fork's schema version and migration registry, plus the Error type
// every fork area returns.
package hub

import "context"

// Init runs the fork's own migrations. It is composed with seeding and notifier
// registration in services/hub's own Init, which is what routers/init.go mounts.
func Init(ctx context.Context) error {
	return Migrate(ctx)
}

// Error is a hub error. It always carries a suggested next action.
type Error struct {
	Message         string
	SuggestedAction string
}

func (e *Error) Error() string { return e.Message + " — " + e.SuggestedAction }
