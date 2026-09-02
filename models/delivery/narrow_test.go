// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"
	"errors"
	"reflect"
	"testing"

	actions_model "gitea.dev/models/actions"
	"gitea.dev/models/db"
	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise NarrowSecretsToJobEnvironment itself — the exported function the
// spoke at models/secret/secret.go names — not the applyEnvironmentScope helper underneath
// it. Testing only the helper leaves the shipped path unverified: the tested unit and the
// production path have to be the same function.

const (
	prodSecret   = "PROD_DB_PASS"
	qaSecret     = "QA_DB_PASS"
	sharedSecret = "SHARED_API_KEY"
)

func taskSecrets() map[string]string {
	return map[string]string{
		"GITHUB_TOKEN": "auto-github",
		"GITEA_TOKEN":  "auto-gitea",
		prodSecret:     "prod-value",
		qaSecret:       "qa-value",
		sharedSecret:   "shared-value",
	}
}

// withDeps swaps the narrowing's lookups for the duration of one test.
func withDeps(t *testing.T, deps narrowDeps) {
	t.Helper()
	previous := narrowSecretDeps
	narrowSecretDeps = deps
	t.Cleanup(func() { narrowSecretDeps = previous })
}

// stubDeps resolves a run for repo 7, the given scope table and the given environment.
func stubDeps(scopes map[string]string, env string) narrowDeps {
	return narrowDeps{
		loadRun: func(_ context.Context, job *actions_model.ActionRunJob) error {
			job.Run = &actions_model.ActionRun{ID: 1, RepoID: 7}
			return nil
		},
		scopesOf:    func(context.Context, int64) (map[string]string, error) { return scopes, nil },
		environment: func(context.Context, *actions_model.ActionRunJob) (string, error) { return env, nil },
	}
}

var repoScopes = map[string]string{prodSecret: "prod", qaSecret: "qa"}

// TestNarrowSecretsToJobEnvironment asserts scoping against the shipped entry point:
// PROD_DB_PASS scoped to prod is present under environment: prod, absent from a job
// declaring environment: qa, and absent from a job declaring no environment at all.
func TestNarrowSecretsToJobEnvironment(t *testing.T) {
	cases := []struct {
		name    string
		jobEnv  string
		present []string
		absent  []string
	}{
		{
			name: "job declares prod", jobEnv: "prod",
			present: []string{prodSecret, sharedSecret, "GITHUB_TOKEN", "GITEA_TOKEN"},
			absent:  []string{qaSecret},
		},
		{
			name: "job declares a different environment", jobEnv: "qa",
			present: []string{qaSecret, sharedSecret, "GITHUB_TOKEN", "GITEA_TOKEN"},
			absent:  []string{prodSecret},
		},
		{
			name: "job declares no environment", jobEnv: "",
			present: []string{sharedSecret, "GITHUB_TOKEN", "GITEA_TOKEN"},
			absent:  []string{prodSecret, qaSecret},
		},
		{
			name: "job declares an environment nothing is scoped to", jobEnv: "staging",
			present: []string{sharedSecret, "GITHUB_TOKEN", "GITEA_TOKEN"},
			absent:  []string{prodSecret, qaSecret},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withDeps(t, stubDeps(repoScopes, c.jobEnv))
			got := NarrowSecretsToJobEnvironment(t.Context(), &actions_model.ActionRunJob{ID: 42}, taskSecrets())
			for _, name := range c.present {
				assert.Contains(t, got, name, "%s must resolve for a job declaring %q", name, c.jobEnv)
				assert.Equal(t, taskSecrets()[name], got[name], "%s must keep its value", name)
			}
			for _, name := range c.absent {
				assert.NotContains(t, got, name, "%s must be unreachable from a job declaring %q", name, c.jobEnv)
			}
		})
	}
}

// TestNarrowSecretsLeavesAnUnscopedRepositoryAlone is the compatibility guarantee:
// with nothing scoped, adding the fork changes no existing behaviour.
func TestNarrowSecretsLeavesAnUnscopedRepositoryAlone(t *testing.T) {
	withDeps(t, stubDeps(map[string]string{}, "prod"))
	secrets := taskSecrets()
	assert.Equal(t, secrets, NarrowSecretsToJobEnvironment(t.Context(), &actions_model.ActionRunJob{ID: 42}, secrets))
}

// TestNarrowSecretsFailsClosed covers each way the narrowing can fail to establish the
// job's scope. Two of these branches previously returned the secret set unchanged, which
// handed PROD_DB_PASS to any job whenever a lookup failed.
func TestNarrowSecretsFailsClosed(t *testing.T) {
	boom := errors.New("database is unreachable")

	cases := []struct {
		name string
		deps narrowDeps
		job  *actions_model.ActionRunJob
		// whether a scoped secret may still resolve; only the environment-lookup failure
		// keeps the scope table, so it can distinguish scoped from unscoped.
		keepsUnscoped bool
	}{
		{
			name: "the run cannot be loaded",
			deps: narrowDeps{
				loadRun:     func(context.Context, *actions_model.ActionRunJob) error { return boom },
				scopesOf:    func(context.Context, int64) (map[string]string, error) { return repoScopes, nil },
				environment: func(context.Context, *actions_model.ActionRunJob) (string, error) { return "prod", nil },
			},
			job: &actions_model.ActionRunJob{ID: 42},
		},
		{
			name: "the scope table cannot be read",
			deps: narrowDeps{
				loadRun: func(_ context.Context, job *actions_model.ActionRunJob) error {
					job.Run = &actions_model.ActionRun{ID: 1, RepoID: 7}
					return nil
				},
				scopesOf:    func(context.Context, int64) (map[string]string, error) { return nil, boom },
				environment: func(context.Context, *actions_model.ActionRunJob) (string, error) { return "prod", nil },
			},
			job: &actions_model.ActionRunJob{ID: 42},
		},
		{
			name: "there is no job to establish an environment for",
			deps: stubDeps(repoScopes, "prod"),
			job:  nil,
		},
		{
			name: "the job's environment cannot be resolved",
			deps: narrowDeps{
				loadRun: func(_ context.Context, job *actions_model.ActionRunJob) error {
					job.Run = &actions_model.ActionRun{ID: 1, RepoID: 7}
					return nil
				},
				scopesOf:    func(context.Context, int64) (map[string]string, error) { return repoScopes, nil },
				environment: func(context.Context, *actions_model.ActionRunJob) (string, error) { return "", boom },
			},
			job:           &actions_model.ActionRunJob{ID: 42},
			keepsUnscoped: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withDeps(t, c.deps)
			got := NarrowSecretsToJobEnvironment(t.Context(), c.job, taskSecrets())

			assert.NotContains(t, got, prodSecret, "a production credential must never survive a failed lookup")
			assert.NotContains(t, got, qaSecret, "no environment-scoped secret survives a failed lookup")
			assert.Equal(t, "auto-github", got["GITHUB_TOKEN"], "the per-task tokens are generated, not configured, and always survive")
			assert.Equal(t, "auto-gitea", got["GITEA_TOKEN"])

			if c.keepsUnscoped {
				assert.Contains(t, got, sharedSecret, "the scope table was readable, so an unscoped secret is known to be unscoped")
			} else {
				assert.NotContains(t, got, sharedSecret,
					"with the scope table unreadable, an unscoped secret cannot be told apart from one scoped to production")
				assert.Len(t, got, 2, "only the per-task tokens remain")
			}
		})
	}
}

// TestFailClosedKeepsOnlyGeneratedTokens pins the helper the branches above rely on.
func TestFailClosedKeepsOnlyGeneratedTokens(t *testing.T) {
	assert.Equal(t, map[string]string{"GITHUB_TOKEN": "auto-github", "GITEA_TOKEN": "auto-gitea"},
		failClosed(taskSecrets()))
	assert.Empty(t, failClosed(map[string]string{prodSecret: "v"}))
	assert.Empty(t, failClosed(nil))
}

// TestProductionNarrowDepsAreWired proves the seam is wired to the real lookups, so a test
// that swaps them is still testing the shipped path.
func TestProductionNarrowDepsAreWired(t *testing.T) {
	require.NotNil(t, narrowSecretDeps.loadRun)
	require.NotNil(t, narrowSecretDeps.scopesOf)
	require.NotNil(t, narrowSecretDeps.environment)

	assert.Equal(t, reflect.ValueOf(productionNarrowDeps.scopesOf).Pointer(), reflect.ValueOf(narrowSecretDeps.scopesOf).Pointer())
	assert.Equal(t, reflect.ValueOf(productionNarrowDeps.environment).Pointer(), reflect.ValueOf(narrowSecretDeps.environment).Pointer())
	assert.Equal(t, reflect.ValueOf(scopesForRepo).Pointer(), reflect.ValueOf(productionNarrowDeps.scopesOf).Pointer(),
		"the default scope lookup must be the one that reads delivery_secret_scope")
	assert.Equal(t, reflect.ValueOf(JobEnvironment).Pointer(), reflect.ValueOf(productionNarrowDeps.environment).Pointer(),
		"the default environment lookup must be the one that re-reads the workflow file")
}

// TestNarrowSecretsThroughTheProductionDeps runs the entry point with NO seam replacement,
// against SQLite: the real LoadRun, the real scopesForRepo reading delivery_secret_scope,
// and the real JobEnvironment. The run records no workflow source, so JobEnvironment
// refuses and the environment-scoped secrets are withheld.
func TestNarrowSecretsThroughTheProductionDeps(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	const repoID int64 = 7
	require.NoError(t, db.Insert(ctx, &SecretScope{RepoID: repoID, SecretName: prodSecret, Environment: "prod"}))
	require.NoError(t, db.Insert(ctx, &SecretScope{RepoID: repoID, SecretName: qaSecret, Environment: "qa"}))

	// Run preloaded, so the real LoadRun is satisfied without a run fixture; WorkflowRepoID
	// is zero, so the real JobEnvironment returns ErrNoWorkflowSource.
	job := &actions_model.ActionRunJob{ID: 42, RunID: 1, RepoID: repoID, JobID: "deploy", Run: &actions_model.ActionRun{ID: 1, RepoID: repoID}}

	got := NarrowSecretsToJobEnvironment(ctx, job, taskSecrets())
	assert.NotContains(t, got, prodSecret, "an unresolvable environment withholds every scoped secret")
	assert.NotContains(t, got, qaSecret)
	assert.Contains(t, got, sharedSecret, "the scope table was read, so an unscoped secret is known to be unscoped")
	assert.Contains(t, got, "GITHUB_TOKEN")

	// A repository with no scope rows is left exactly as it was.
	untouched := &actions_model.ActionRunJob{ID: 43, RunID: 2, RepoID: 999, JobID: "build", Run: &actions_model.ActionRun{ID: 2, RepoID: 999}}
	secrets := taskSecrets()
	assert.Equal(t, secrets, NarrowSecretsToJobEnvironment(ctx, untouched, secrets))
}

// TestScopesForRepo covers the real scope lookup the narrowing depends on.
func TestScopesForRepo(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	require.NoError(t, db.Insert(ctx, &SecretScope{RepoID: 11, SecretName: prodSecret, Environment: "prod"}))
	require.NoError(t, db.Insert(ctx, &SecretScope{RepoID: 11, SecretName: qaSecret, Environment: "qa"}))
	require.NoError(t, db.Insert(ctx, &SecretScope{RepoID: 12, SecretName: prodSecret, Environment: "prod"}))

	scopes, err := scopesForRepo(ctx, 11)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{prodSecret: "prod", qaSecret: "qa"}, scopes)

	other, err := scopesForRepo(ctx, 12)
	require.NoError(t, err)
	assert.Len(t, other, 1, "a scope row belongs to one repository only")

	empty, err := scopesForRepo(ctx, 13)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// TestFindSecretScopes covers the listing the secrets endpoint pages over.
func TestFindSecretScopes(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	for _, name := range []string{"A_SECRET", "B_SECRET", "C_SECRET"} {
		require.NoError(t, db.Insert(ctx, &SecretScope{RepoID: 21, SecretName: name, Environment: "prod"}))
	}
	require.NoError(t, db.Insert(ctx, &SecretScope{RepoID: 21, SecretName: "D_SECRET", Environment: "qa"}))

	rows, total, err := FindSecretScopes(ctx, builderEq("repo_id", 21).And(builderEq("environment", "prod")), "secret_name ASC, id ASC", 2, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total, "the count is of every matching row, not of the page")
	require.Len(t, rows, 2, "the page is limited")
	assert.Equal(t, "A_SECRET", rows[0].SecretName)
	assert.Equal(t, "B_SECRET", rows[1].SecretName)

	page2, _, err := FindSecretScopes(ctx, builderEq("repo_id", 21).And(builderEq("environment", "prod")), "secret_name ASC, id ASC", 2, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, "C_SECRET", page2[0].SecretName)
}

// TestFindEnvironments covers the listing every environment endpoint pages over.
func TestFindEnvironments(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	envs, total, err := FindEnvironments(ctx, builderEq("repo_id", DefaultsRepoID), "sort_order ASC, id ASC", 2, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 5, total, "models/fixtures/delivery_environment.yml defines five default rows")
	require.Len(t, envs, 2)
	assert.Equal(t, "dev", envs[0].Name)
	assert.Equal(t, "qa", envs[1].Name)

	all, _, err := FindEnvironments(ctx, builderEq("repo_id", DefaultsRepoID), "sort_order DESC, id DESC", 0, 0)
	require.NoError(t, err)
	require.NotEmpty(t, all)
	assert.Equal(t, "prod", all[0].Name, "the order the caller asked for is the order it gets")
}
