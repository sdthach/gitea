// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"context"
	"time"

	deployments_model "gitea.dev/models/deployments"
	hub_model "gitea.dev/models/hub"
	"gitea.dev/modules/log"
	deployments_service "gitea.dev/services/deployments"
	"gitea.dev/services/notify"
)

// pageTokenSweepInterval is how often Init's goroutine sweeps expired page tokens.
const pageTokenSweepInterval = time.Minute

// pageTokenMaxAge is how long a page token survives since it was minted before the sweep
// deletes it.
const pageTokenMaxAge = 24 * time.Hour

// waitingSweepInterval is how often Init's goroutine re-evaluates waiting deployments — a
// wait timer or a deployment window is measured in minutes, so a minute is fine granularity
// without polling the table needlessly.
const waitingSweepInterval = time.Minute

// nowFunc is what the waiting-deployment sweep calls "now". A test overrides it to move the
// clock without sleeping past a real wait timer or deployment window.
var nowFunc = time.Now

// Init mounts the fork: it runs the hub's own migrations and seeds the default environment
// set, then registers the deployment notifier. It also starts a goroutine sweeping expired
// page tokens, which runs until ctx is done.
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

	go runSweeper(ctx, pageTokenSweepInterval, func() {
		if _, err := hub_model.SweepPageTokens(ctx, time.Now().Add(-pageTokenMaxAge)); err != nil {
			log.Error("hub: sweep page tokens: %v", err)
		}
	})

	go runSweeper(ctx, waitingSweepInterval, func() { sweepWaitingDeployments(ctx) })

	return nil
}

// sweepWaitingDeployments re-evaluates every waiting deployment at nowFunc's current answer.
// Pulled out of Init's closure so a test can drive one sweep directly, with nowFunc overridden,
// instead of waiting on the real ticker.
func sweepWaitingDeployments(ctx context.Context) {
	if err := deployments_service.ReevaluateWaiting(ctx, nowFunc().Unix()); err != nil {
		log.Error("hub: re-evaluate waiting deployments: %v", err)
	}
}
