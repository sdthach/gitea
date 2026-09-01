// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"
	"slices"
	"strings"

	actions_model "gitea.dev/models/actions"
	"gitea.dev/models/db"
	"gitea.dev/modules/log"
	"gitea.dev/modules/timeutil"

	"xorm.io/builder"
)

// autoTokens are generated per task rather than configured, so they are never scoped and
// never narrowed away.
var autoTokens = []string{"GITHUB_TOKEN", "GITEA_TOKEN"}

// SecretScope binds a repository secret name to one environment (F4). It is a fork table:
// upstream's Secret is unique on (OwnerID, RepoID, Name) with no environment dimension, and
// adding a column to it would be a second edit to an upstream file (F2).
type SecretScope struct {
	ID          int64              `xorm:"pk autoincr" json:"id"`
	RepoID      int64              `xorm:"INDEX UNIQUE(repo_secret) NOT NULL" json:"repo_id"`
	SecretName  string             `xorm:"VARCHAR(255) UNIQUE(repo_secret) NOT NULL" json:"name"`
	Environment string             `xorm:"VARCHAR(64) NOT NULL" json:"environment"`
	CreatedUnix timeutil.TimeStamp `xorm:"created NOT NULL" json:"created_unix"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated NOT NULL" json:"updated_unix"`
}

func (*SecretScope) TableName() string { return "delivery_secret_scope" }

func init() {
	db.RegisterModel(new(SecretScope))
}

// NormalizeSecretName matches how Gitea stores Actions secret names.
func NormalizeSecretName(name string) string { return strings.ToUpper(strings.TrimSpace(name)) }

// applyEnvironmentScope drops every secret scoped to an environment other than jobEnv, and
// every scoped secret when the job declares no environment. It is pure so SC 17's three
// cases are testable without a database, a runner or a network.
//
// scopes maps secret name to the environment it is scoped to. A secret with no entry is
// unscoped and always resolves, which is why adding the fork changes no existing behaviour.
func applyEnvironmentScope(secrets, scopes map[string]string, jobEnv string) map[string]string {
	jobEnv = NormalizeEnvironmentName(jobEnv)

	normalized := make(map[string]string, len(scopes))
	for name, env := range scopes {
		normalized[NormalizeSecretName(name)] = NormalizeEnvironmentName(env)
	}

	out := make(map[string]string, len(secrets))
	for name, value := range secrets {
		scope, scoped := normalized[NormalizeSecretName(name)]
		switch {
		case isAutoToken(name):
			out[name] = value
		case !scoped:
			out[name] = value
		case jobEnv != "" && scope == jobEnv:
			out[name] = value
		}
	}
	return out
}

func isAutoToken(name string) bool { return slices.Contains(autoTokens, name) }

// FindSecretScopes lists the scope rows matching cond.
func FindSecretScopes(ctx context.Context, cond builder.Cond, orderBy string, limit, offset int) ([]*SecretScope, int64, error) {
	sess := db.GetEngine(ctx).Where(cond).OrderBy(orderBy)
	if limit > 0 {
		sess = sess.Limit(limit, offset)
	}
	scopes := make([]*SecretScope, 0, 8)
	count, err := sess.FindAndCount(&scopes)
	if err != nil {
		return nil, 0, err
	}
	return scopes, count, nil
}

// scopesForRepo reads the repository's scope rows as a name -> environment map.
func scopesForRepo(ctx context.Context, repoID int64) (map[string]string, error) {
	rows := make([]*SecretScope, 0, 8)
	if err := db.GetEngine(ctx).Where("repo_id = ?", repoID).Find(&rows); err != nil {
		return nil, err
	}
	scopes := make(map[string]string, len(rows))
	for _, r := range rows {
		scopes[r.SecretName] = r.Environment
	}
	return scopes, nil
}

// failClosed drops every configured secret, keeping only the per-task tokens Gitea
// generates. It is what the narrowing returns when it cannot establish which secrets are
// scoped: with the scope table unreadable, an unscoped secret cannot be told apart from one
// scoped to production, and returning the set unchanged would hand a production credential
// to whatever job asked.
func failClosed(secrets map[string]string) map[string]string {
	out := make(map[string]string, len(autoTokens))
	for _, name := range autoTokens {
		if value, ok := secrets[name]; ok {
			out[name] = value
		}
	}
	return out
}

// narrowDeps are the three lookups the narrowing performs. They are a struct of functions
// rather than direct calls so that every branch — including the ones that run only when a
// lookup fails — is reachable from a unit test with no database, no git repository and no
// network (J10, J11).
type narrowDeps struct {
	loadRun     func(context.Context, *actions_model.ActionRunJob) error
	scopesOf    func(ctx context.Context, repoID int64) (map[string]string, error)
	environment func(context.Context, *actions_model.ActionRunJob) (string, error)
}

// productionNarrowDeps is the wiring the running binary uses.
var productionNarrowDeps = narrowDeps{
	loadRun:     func(ctx context.Context, job *actions_model.ActionRunJob) error { return job.LoadRun(ctx) },
	scopesOf:    scopesForRepo,
	environment: JobEnvironment,
}

// narrowSecretDeps is what NarrowSecretsToJobEnvironment calls through. Tests replace it and
// restore it; nothing else writes to it.
var narrowSecretDeps = productionNarrowDeps

// NarrowSecretsToJobEnvironment is the fork's tail of models/secret.GetSecretsOfTask — the
// single existing chokepoint every task's secrets already pass through (F4). It is the
// function the spoke at models/secret/secret.go names.
//
// It fails closed. When the job's declared environment cannot be resolved, every
// environment-scoped secret is dropped; when the scope table itself cannot be read, every
// configured secret is dropped and only the per-task tokens remain. A deploy failing for a
// missing secret is recoverable; a production credential reaching a job that did not
// declare production is not.
func NarrowSecretsToJobEnvironment(ctx context.Context, job *actions_model.ActionRunJob, secrets map[string]string) map[string]string {
	if job == nil {
		// Nothing identifies the job, so nothing establishes its environment.
		log.Error("delivery: narrowing called with no job — withholding every configured secret; this is a programming error, report it with the run that triggered it")
		return failClosed(secrets)
	}
	if err := narrowSecretDeps.loadRun(ctx, job); err != nil {
		log.Error("delivery: load run of job %d: %v — withholding every configured secret; check the database is reachable and rerun the job", job.ID, err)
		return failClosed(secrets)
	}

	scopes, err := narrowSecretDeps.scopesOf(ctx, job.Run.RepoID)
	if err != nil {
		log.Error("delivery: read secret scopes of repo %d: %v — withholding every configured secret; check the database is reachable and rerun the job", job.Run.RepoID, err)
		return failClosed(secrets)
	}
	if len(scopes) == 0 {
		// Nothing in this repository is scoped, so adding the fork changes no behaviour.
		return secrets
	}

	jobEnv, err := narrowSecretDeps.environment(ctx, job)
	if err != nil {
		log.Error("delivery: resolve environment of job %d: %v — withholding every environment-scoped secret; check the workflow file is readable at the run's commit", job.ID, err)
		jobEnv = ""
	}
	return applyEnvironmentScope(secrets, scopes, jobEnv)
}
