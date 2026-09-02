// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	actions_model "gitea.dev/models/actions"
	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	"gitea.dev/models/delivery"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deployWorkflowContent declares one job per case SC 17 names.
const deployWorkflowContent = `name: deploy
on: workflow_dispatch
jobs:
  to-prod:
    runs-on: ubuntu-latest
    environment: prod
    steps:
      - run: ./deploy.sh
  to-qa:
    runs-on: ubuntu-latest
    environment: qa
    steps:
      - run: ./deploy.sh
  build:
    runs-on: ubuntu-latest
    steps:
      - run: make
`

// commitWorkflow writes the workflow onto the repository's default branch and returns the
// repository and the commit it landed on.
func commitWorkflow(t *testing.T) (*repo_model.Repository, string) {
	t.Helper()
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1, OwnerID: user.ID})

	resp := testCreateFileInBranch(t, user, repo, createFileInBranchOptions{
		OldBranch:     repo.DefaultBranch,
		CommitMessage: "add a deploy workflow",
	}, map[string]string{".gitea/workflows/deploy.yaml": deployWorkflowContent})
	require.NotNil(t, resp.Commit)
	require.NotEmpty(t, resp.Commit.SHA)
	return repo, resp.Commit.SHA
}

func deployJob(repo *repo_model.Repository, sha, jobID string) *actions_model.ActionRunJob {
	return &actions_model.ActionRunJob{
		ID: 9001, RunID: 9001, RepoID: repo.ID, JobID: jobID,
		Run: &actions_model.ActionRun{
			ID: 9001, RepoID: repo.ID, WorkflowID: "deploy.yaml",
			WorkflowRepoID: repo.ID, WorkflowCommitSHA: sha,
		},
	}
}

// TestDeliveryJobEnvironmentReadsTheWorkflowFile covers the resolution path the fork has to
// take because jobparser.Job carries no environment field at the pin: the declaration is
// re-read from the workflow file at the commit the run records.
func TestDeliveryJobEnvironmentReadsTheWorkflowFile(t *testing.T) {
	// Writing the workflow goes through Gitea's own push path, whose pre-receive hook calls
	// back into the internal API, so the server has to be running.
	onGiteaRun(t, func(t *testing.T, _ *url.URL) {
		repo, sha := commitWorkflow(t)

		for jobID, want := range map[string]string{"to-prod": "prod", "to-qa": "qa", "build": ""} {
			t.Run(jobID, func(t *testing.T) {
				got, err := delivery.JobEnvironment(t.Context(), deployJob(repo, sha, jobID))
				require.NoError(t, err)
				assert.Equal(t, want, got)
			})
		}

		t.Run("a run recording no workflow source is refused", func(t *testing.T) {
			job := deployJob(repo, sha, "to-prod")
			job.Run.WorkflowCommitSHA = ""
			_, err := delivery.JobEnvironment(t.Context(), job)
			require.ErrorIs(t, err, delivery.ErrNoWorkflowSource)
		})

		t.Run("a workflow the commit does not carry is refused", func(t *testing.T) {
			job := deployJob(repo, sha, "to-prod")
			job.Run.WorkflowID = "absent.yaml"
			_, err := delivery.JobEnvironment(t.Context(), job)
			require.ErrorIs(t, err, delivery.ErrNoWorkflowSource)
		})
	})
}

// TestDeliverySecretNarrowingEndToEnd is SC 17 through the production path: a real
// workflow file in a real repository, the real scope table, and the exported function the
// spoke in models/secret/secret.go calls, with no dependency injected.
func TestDeliverySecretNarrowingEndToEnd(t *testing.T) {
	onGiteaRun(t, func(t *testing.T, _ *url.URL) {
		repo, sha := commitWorkflow(t)
		ctx := t.Context()

		require.NoError(t, db.Insert(ctx, &delivery.SecretScope{RepoID: repo.ID, SecretName: "PROD_DB_PASS", Environment: "prod"}))
		require.NoError(t, db.Insert(ctx, &delivery.SecretScope{RepoID: repo.ID, SecretName: "QA_DB_PASS", Environment: "qa"}))

		secrets := func() map[string]string {
			return map[string]string{
				"GITHUB_TOKEN":   "auto",
				"GITEA_TOKEN":    "auto",
				"PROD_DB_PASS":   "prod-value",
				"QA_DB_PASS":     "qa-value",
				"SHARED_API_KEY": "shared-value",
			}
		}

		prod := delivery.NarrowSecretsToJobEnvironment(ctx, deployJob(repo, sha, "to-prod"), secrets())
		assert.Equal(t, "prod-value", prod["PROD_DB_PASS"], "a job declaring environment: prod resolves the production secret")
		assert.NotContains(t, prod, "QA_DB_PASS")
		assert.Contains(t, prod, "SHARED_API_KEY")

		qa := delivery.NarrowSecretsToJobEnvironment(ctx, deployJob(repo, sha, "to-qa"), secrets())
		assert.NotContains(t, qa, "PROD_DB_PASS", "PROD_DB_PASS is absent from a job declaring environment: qa (SC 17)")
		assert.Equal(t, "qa-value", qa["QA_DB_PASS"])

		none := delivery.NarrowSecretsToJobEnvironment(ctx, deployJob(repo, sha, "build"), secrets())
		assert.NotContains(t, none, "PROD_DB_PASS", "PROD_DB_PASS is absent from a job declaring no environment (SC 17)")
		assert.NotContains(t, none, "QA_DB_PASS")
		assert.Contains(t, none, "SHARED_API_KEY", "an unscoped secret is unaffected")
	})
}

// TestDeliverySecretScopeWritesAreRepoAdminOnly covers the pair of endpoints that make F4
// configurable at all: binding a secret name to an environment, and unbinding it. Neither
// ever accepts or returns a secret value (I12).
func TestDeliverySecretScopeWritesAreRepoAdminOnly(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	ownerToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	// user4 can read user2/repo1, which is public, and does not administer it.
	req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/secret-scopes",
		map[string]any{"repo_id": 1, "secret_name": "DEPLOY_KEY", "environment": "prod"}).AddTokenAuth(outsiderToken)
	var refusal deliveryRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusForbidden), &refusal)
	assert.Equal(t, "forbidden", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action (A21)")
	// A refused bind writes nothing.
	unittest.AssertNotExistsBean(t, &delivery.SecretScope{RepoID: 1, SecretName: "DEPLOY_KEY"})

	req = NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/secret-scopes",
		map[string]any{"repo_id": 1, "secret_name": "DEPLOY_KEY", "environment": "prod"}).AddTokenAuth(ownerToken)
	var created struct {
		ID          int64  `json:"id"`
		SecretName  string `json:"name"`
		Environment string `json:"environment"`
	}
	DecodeJSON(t, MakeRequest(t, req, http.StatusCreated), &created)
	assert.Equal(t, "DEPLOY_KEY", created.SecretName)
	assert.Equal(t, "prod", created.Environment)
	require.Positive(t, created.ID)

	listing := MakeRequest(t, NewRequest(t, "GET", deliveryv1.BasePath+
		"/repos/user2/repo1/environments/prod/secrets").AddTokenAuth(ownerToken), http.StatusOK)
	var listed []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	DecodeJSON(t, listing, &listed)
	require.Len(t, listed, 1)
	assert.Equal(t, "DEPLOY_KEY", listed[0].Name, "the bound name is listed against its environment")
	assert.Equal(t, created.ID, listed[0].ID,
		"the listing carries the id the unbind endpoint takes, so a page can offer Unbind after a reload")

	req = NewRequest(t, "DELETE", fmt.Sprintf("%s/secret-scopes/%d", deliveryv1.BasePath, created.ID)).
		AddTokenAuth(outsiderToken)
	MakeRequest(t, req, http.StatusForbidden)
	// A refused unbind removes nothing.
	unittest.AssertExistsAndLoadBean(t, &delivery.SecretScope{ID: created.ID})

	req = NewRequest(t, "DELETE", fmt.Sprintf("%s/secret-scopes/%d", deliveryv1.BasePath, created.ID)).
		AddTokenAuth(ownerToken)
	MakeRequest(t, req, http.StatusNoContent)
	unittest.AssertNotExistsBean(t, &delivery.SecretScope{ID: created.ID})
}
