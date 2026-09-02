// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package delivery is the fork's hub. `delivery` is the domain; environment,
// deployment, approval and the audit log are aggregates within it, one file each. Every
// edit the fork makes to an upstream file is a single-line delegation into this package,
// so a rebase onto a newer upstream pin is mechanical.
package delivery

import "context"

// Init runs the fork's own migrations and seeds the default environment set. It is mounted
// from routers/init.go's InitWebInstalled, which is the single hub-mount spoke.
func Init(ctx context.Context) error {
	if err := Migrate(ctx); err != nil {
		return err
	}
	return Seed(ctx, SeededEnvironments())
}
