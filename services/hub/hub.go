// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"context"

	deployments_model "gitea.dev/models/deployments"
	hub_model "gitea.dev/models/hub"
	deployments_service "gitea.dev/services/deployments"
	"gitea.dev/services/notify"
)

// Init mounts the fork: it runs the hub's own migrations and seeds the default environment
// set, then registers the deployment notifier.
//
// The notifier is registered here rather than from models/deployments because services/notify
// sits above the models layer; registering from the model package would invert the
// dependency.
func Init(ctx context.Context) error {
	if err := hub_model.Init(ctx); err != nil {
		return err
	}
	if err := deployments_model.Seed(ctx, deployments_model.SeededEnvironments()); err != nil {
		return err
	}
	notify.RegisterNotifier(deployments_service.NewNotifier())
	return nil
}
