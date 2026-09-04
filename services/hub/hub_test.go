// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"testing"
	"time"

	"gitea.dev/models/db"
	deployments_model "gitea.dev/models/deployments"
	hub_model "gitea.dev/models/hub"
	"gitea.dev/models/unittest"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xorm.io/builder"
)

func TestInitMigratesAndSeeds(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	_, err := db.GetEngine(ctx).Where("1=1").Delete(new(deployments_model.Environment))
	require.NoError(t, err)
	_, err = db.GetEngine(ctx).Where("1=1").Delete(new(hub_model.Version))
	require.NoError(t, err)

	previous := setting.CfgProvider
	t.Cleanup(func() { setting.CfgProvider = previous })
	cfg, err := setting.NewConfigProviderFromData("[delivery]\nDEFAULT_ENVIRONMENTS = sandbox, live\n")
	require.NoError(t, err)
	setting.CfgProvider = cfg

	require.NoError(t, Init(ctx))

	v := new(hub_model.Version)
	has, err := db.GetEngine(ctx).Where("1=1").Get(v)
	require.NoError(t, err)
	require.True(t, has)
	assert.Equal(t, hub_model.RegisteredMigrations()[len(hub_model.RegisteredMigrations())-1].ID, v.Version, "Init migrates")

	count, err := db.GetEngine(ctx).Where("repo_id = ?", deployments_model.DefaultsRepoID).Count(new(deployments_model.Environment))
	require.NoError(t, err)
	assert.EqualValues(t, 2, count, "Init seeds the configured set")
	seeded, err := db.GetEngine(ctx).Where("repo_id = ? AND admins_can_bypass = ?", deployments_model.DefaultsRepoID, true).Count(new(deployments_model.Environment))
	require.NoError(t, err)
	assert.EqualValues(t, 2, seeded, "seeded environments let administrators bypass, as they did before")

	// Booting twice is safe.
	require.NoError(t, Init(ctx))
	count, err = db.GetEngine(ctx).Where("repo_id = ?", deployments_model.DefaultsRepoID).Count(new(deployments_model.Environment))
	require.NoError(t, err)
	assert.EqualValues(t, 2, count, "a second boot adds nothing")

	setting.CfgProvider = previous
	_, err = db.GetEngine(ctx).Where("1=1").Delete(new(deployments_model.Environment))
	require.NoError(t, err)
	require.NoError(t, Init(ctx))
	count, err = db.GetEngine(ctx).Count(new(deployments_model.Environment))
	require.NoError(t, err)
	assert.Zero(t, count, "with no configured set Init creates no environment")
}

// TestSweepWaitingDeploymentsUsesNowFunc drives the waiting sweeper directly, with nowFunc
// overridden to a moment 11 minutes after the placeholder was created: only the fake clock,
// never real elapsed time, is what could let a 10-minute wait timer already look elapsed.
func TestSweepWaitingDeploymentsUsesNowFunc(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	env := &deployments_model.Environment{
		RepoID: 1, Name: "hub-sweep-prod", ReviewPolicy: deployments_model.PolicyNone, RequiredReviewers: 1, WaitMinutes: 10,
	}
	require.NoError(t, deployments_model.ValidateEnvironment(env))
	require.NoError(t, db.Insert(ctx, env))

	placeholder := &deployments_model.Deployment{RepoID: 1, Environment: env.Name, ReleaseTag: "v1.1"}
	require.NoError(t, deployments_model.AppendPlaceholderDeployment(ctx, placeholder))
	requested := timeutil.TimeStampNow()

	previous := nowFunc
	t.Cleanup(func() { nowFunc = previous })
	nowFunc = func() time.Time { return requested.AsTime().Add(11 * time.Minute) }

	sweepWaitingDeployments(ctx)

	events, err := deployments_model.FindAuditEvents(ctx, builder.Eq{
		"repo_id": 1, "environment": env.Name, "event": deployments_model.AuditChecksPassed,
	}, "id ASC", 0)
	require.NoError(t, err)
	assert.Len(t, events, 1, "the fake clock, not real time, is what let the 10-minute wait timer look elapsed")
}
