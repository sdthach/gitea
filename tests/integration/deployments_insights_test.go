// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	actions_model "gitea.dev/models/actions"
	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	deployments_model "gitea.dev/models/deployments"
	"gitea.dev/modules/timeutil"
	deploymentsv1 "gitea.dev/routers/api/deployments/v1"
	deployments_service "gitea.dev/services/deployments"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The CI insights' integration tests run under Gitea's own harness, so they execute the way
// upstream's do and survive a rebase.
//
// Repository 4 is public and carries the Actions unit; repository 3 is private to org3. That
// pair is what lets the permission test fail in both directions: a user outside org3 must see
// the first and not the second.
const (
	ciPublicRepoID  = 4
	ciPrivateRepoID = 3
)

// seedCIRun appends one Actions run so the aggregate has something of the test's own to count.
// The shared fixtures say not to grow, so each test brings its own rows.
func seedCIRun(t *testing.T, repoID, index int64, workflow string, status actions_model.Status, duration int64) *actions_model.ActionRun {
	t.Helper()
	now := time.Now().Unix()
	run := &actions_model.ActionRun{
		Title:      fmt.Sprintf("hub overview seed %d/%d", repoID, index),
		RepoID:     repoID,
		OwnerID:    1,
		WorkflowID: workflow,
		Index:      index,
		Ref:        "refs/heads/main",
		CommitSHA:  "c2d72f548424103f01ee1dc02889c1e2bff816b0",
		Event:      "push",
		Status:     status,
	}
	if duration > 0 {
		run.Started = timeutil.TimeStamp(now)
		run.Stopped = timeutil.TimeStamp(now + duration)
	}
	require.NoError(t, db.Insert(t.Context(), run))
	return run
}

// runRows is one /runs page, decoded.
type runRows []struct {
	ID           int64  `json:"id"`
	RepoID       int64  `json:"repo_id"`
	RepoFullName string `json:"repo_full_name"`
	WorkflowID   string `json:"workflow_id"`
	Status       string `json:"status"`
	RunURL       string `json:"run_url"`
	Duration     int64  `json:"duration_seconds"`
}

func (rows runRows) repoIDs() map[int64]bool {
	out := map[int64]bool{}
	for _, r := range rows {
		out[r.RepoID] = true
	}
	return out
}

func getHubJSON(t *testing.T, token, path string, into any) {
	t.Helper()
	req := NewRequest(t, "GET", deploymentsv1.BasePath+path).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	DecodeJSON(t, resp, into)
}

// TestAPIDeploymentsInsightsExcludesARepositoryTheViewerCannotSee is the security case, in both
// its including and its excluding form. A run in a repository the viewer
// cannot read must appear in no list and in no aggregate.
func TestAPIDeploymentsInsightsExcludesARepositoryTheViewerCannotSee(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	seedCIRun(t, ciPublicRepoID, 9001, "ci.yaml", actions_model.StatusSuccess, 30)
	seedCIRun(t, ciPrivateRepoID, 9002, "secret.yaml", actions_model.StatusSuccess, 30)

	insiderToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeReadRepository)
	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user8"), auth_model.AccessTokenScopeReadRepository)

	var insider runRows
	getHubJSON(t, insiderToken, "/runs?limit=200", &insider)
	insiderRepos := insider.repoIDs()
	require.True(t, insiderRepos[ciPrivateRepoID],
		"the including case has to see the private repository's run, or the excluding case proves nothing")

	var outsider runRows
	getHubJSON(t, outsiderToken, "/runs?limit=200", &outsider)
	outsiderRepos := outsider.repoIDs()
	assert.True(t, outsiderRepos[ciPublicRepoID], "a public repository's runs are visible to everyone signed in")
	assert.False(t, outsiderRepos[ciPrivateRepoID],
		"a run in a repository the viewer cannot read must not appear in the cross-repository list")

	// The same exclusion has to hold for every aggregate, not only for the raw list.
	var insiderRepoStats, outsiderRepoStats []struct {
		RepoID int64 `json:"repo_id"`
	}
	getHubJSON(t, insiderToken, "/insights/repos?limit=200", &insiderRepoStats)
	getHubJSON(t, outsiderToken, "/insights/repos?limit=200", &outsiderRepoStats)

	seen := func(rows []struct {
		RepoID int64 `json:"repo_id"`
	}, id int64,
	) bool {
		for _, r := range rows {
			if r.RepoID == id {
				return true
			}
		}
		return false
	}
	require.True(t, seen(insiderRepoStats, ciPrivateRepoID))
	assert.False(t, seen(outsiderRepoStats, ciPrivateRepoID),
		"the per-repository aggregate is scoped by the same filter as the run list")

	var insiderInsights, outsiderInsights struct {
		Summary struct {
			TotalRuns int64 `json:"total_runs"`
		} `json:"summary"`
	}
	getHubJSON(t, insiderToken, "/insights", &insiderInsights)
	getHubJSON(t, outsiderToken, "/insights", &outsiderInsights)
	assert.Less(t, outsiderInsights.Summary.TotalRuns, insiderInsights.Summary.TotalRuns,
		"the summary counts fewer runs for a viewer who can see fewer repositories")
}

// TestAPIDeploymentsInsightsCountsMatchThePerRepositoryQuery: the aggregate has to
// agree with the same question asked one repository at a time.
func TestAPIDeploymentsInsightsCountsMatchThePerRepositoryQuery(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	seedCIRun(t, ciPublicRepoID, 9101, "ci.yaml", actions_model.StatusSuccess, 30)
	seedCIRun(t, ciPublicRepoID, 9102, "ci.yaml", actions_model.StatusFailure, 60)
	seedCIRun(t, ciPrivateRepoID, 9103, "ci.yaml", actions_model.StatusSuccess, 10)

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeReadRepository)

	var all struct {
		Summary struct {
			TotalRuns int64            `json:"total_runs"`
			Runs      map[string]int64 `json:"runs"`
		} `json:"summary"`
		Previous struct {
			Window struct {
				ToUnix int64 `json:"to_unix"`
			} `json:"window"`
		} `json:"previous"`
	}
	getHubJSON(t, token, "/insights", &all)
	require.Positive(t, all.Summary.TotalRuns)

	var repoStats []struct {
		RepoID    int64 `json:"repo_id"`
		Runs      int64 `json:"runs"`
		Successes int64 `json:"successes"`
		Failures  int64 `json:"failures"`
	}
	getHubJSON(t, token, "/insights/repos?limit=200", &repoStats)

	var summed, successes, failures int64
	for _, r := range repoStats {
		summed += r.Runs
		successes += r.Successes
		failures += r.Failures
	}
	assert.Equal(t, all.Summary.TotalRuns, summed,
		"the summary is the sum of its per-repository rows, so every figure is independently queryable")
	assert.Equal(t, all.Summary.Runs["success"], successes)
	assert.Equal(t, all.Summary.Runs["failure"], failures)

	// Narrowing the composite to one repository gives that repository's own row back.
	var one struct {
		Summary struct {
			TotalRuns int64 `json:"total_runs"`
		} `json:"summary"`
	}
	getHubJSON(t, token, fmt.Sprintf("/insights?repo_id=%d", ciPublicRepoID), &one)
	for _, r := range repoStats {
		if r.RepoID == ciPublicRepoID {
			assert.Equal(t, r.Runs, one.Summary.TotalRuns)
		}
	}

	// The workflows resource carries the same runs, grouped the other way.
	var workflows []struct {
		RepoID int64 `json:"repo_id"`
		Runs   int64 `json:"runs"`
	}
	getHubJSON(t, token, "/workflows?limit=200", &workflows)
	var byWorkflow int64
	for _, w := range workflows {
		byWorkflow += w.Runs
	}
	assert.Equal(t, all.Summary.TotalRuns, byWorkflow,
		"grouping by workflow and grouping by repository count the same runs")
}

// TestAPIDeploymentsRunsAnswerFiltersSortingAndPaging covers filters, sorting and paging,
// including the failed-runs list the CLI reproduces.
func TestAPIDeploymentsRunsAnswerFiltersSortingAndPaging(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	failed := seedCIRun(t, ciPublicRepoID, 9201, "ci.yaml", actions_model.StatusFailure, 60)
	seedCIRun(t, ciPublicRepoID, 9202, "ci.yaml", actions_model.StatusSuccess, 30)

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeReadRepository)

	var failures runRows
	getHubJSON(t, token, "/runs?limit=200&status[eq]=failure", &failures)
	require.NotEmpty(t, failures)
	found := false
	for _, r := range failures {
		assert.Equal(t, "failure", r.Status, "the status filter narrows to the state it names")
		if r.ID == failed.ID {
			found = true
			assert.NotEmpty(t, r.RunURL, "every row links out to Gitea's own run page")
			assert.Equal(t, int64(60), r.Duration)
		}
	}
	assert.True(t, found, "the run seeded as a failure is in the failed list the page shows")

	var byRepo runRows
	getHubJSON(t, token, fmt.Sprintf("/runs?limit=200&repo_id=%d&workflow_id=ci.yaml", ciPublicRepoID), &byRepo)
	require.NotEmpty(t, byRepo)
	for _, r := range byRepo {
		assert.Equal(t, int64(ciPublicRepoID), r.RepoID)
		assert.Equal(t, "ci.yaml", r.WorkflowID)
	}

	var ascending runRows
	req := NewRequest(t, "GET", deploymentsv1.BasePath+"/runs?limit=2&sort_by=id&order=asc").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	DecodeJSON(t, resp, &ascending)
	require.Len(t, ascending, 2)
	assert.Less(t, ascending[0].ID, ascending[1].ID, "sorting is answered, not ignored")
	assert.NotEmpty(t, resp.Header().Get("X-Total-Count"), "offset-paged resources carry X-Total-Count")

	// An unknown state names the offender rather than returning an empty page a caller would
	// read as "nothing failed".
	req = NewRequest(t, "GET", deploymentsv1.BasePath+"/runs?status[eq]=exploded").AddTokenAuth(token)
	rejected := MakeRequest(t, req, http.StatusBadRequest)
	var payload struct {
		Code            string   `json:"code"`
		Message         string   `json:"message"`
		SuggestedAction string   `json:"suggested_action"`
		Accepted        []string `json:"accepted"`
	}
	DecodeJSON(t, rejected, &payload)
	assert.Equal(t, "unknown_run_status", payload.Code)
	assert.Contains(t, payload.Message, "exploded")
	assert.NotEmpty(t, payload.SuggestedAction)
	assert.Contains(t, payload.Accepted, "failure")
}

// TestAPIDeploymentsInsightsTrendMatchesTheDeploymentTables is the deployment half: the trend's
// deployment count is the fork's own deploy_deployment, so the CI dashboard and the hub
// grid share one source of truth.
func TestAPIDeploymentsInsightsTrendMatchesTheDeploymentTables(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	require.NoError(t, deployments_model.AppendDeployment(t.Context(), &deployments_model.Deployment{
		RepoID: ciPublicRepoID, Environment: "qa", ReleaseTag: "v1", RunID: 9301, Status: "success",
	}))
	require.NoError(t, deployments_model.AppendDeployment(t.Context(), &deployments_model.Deployment{
		RepoID: ciPrivateRepoID, Environment: "prod", ReleaseTag: "v1", RunID: 9302, Status: "success",
	}))

	insiderToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeReadRepository)
	outsiderToken := getTokenForLoggedInUser(t, loginUser(t, "user8"), auth_model.AccessTokenScopeReadRepository)

	type point struct {
		Date        string `json:"date"`
		Deployments int64  `json:"deployments"`
	}
	sum := func(points []point) int64 {
		var total int64
		for _, p := range points {
			total += p.Deployments
		}
		return total
	}

	var insider, outsider []point
	getHubJSON(t, insiderToken, "/insights/trends", &insider)
	getHubJSON(t, outsiderToken, "/insights/trends", &outsider)

	require.NotEmpty(t, insider, "the series has one point per UTC day, including quiet ones")
	assert.Equal(t, int64(2), sum(insider), "both deployments are counted for a viewer who can see both repositories")
	assert.Equal(t, int64(1), sum(outsider),
		"the deployment in a repository the viewer cannot read reaches no trend")

	var deployments []struct {
		RepoID int64 `json:"repo_id"`
	}
	getHubJSON(t, insiderToken, "/deployments?limit=200", &deployments)
	assert.Len(t, deployments, int(sum(insider)),
		"the trend's deployment count is the deployments resource, read back")
}

// TestDeploymentsInsightsPageIsAClientOfItsAPI covers the new page: it sits behind
// sign-in and serves only the shell, fetching every figure from documented endpoints.
func TestDeploymentsInsightsPageIsAClientOfItsAPI(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/deployments/insights")
	MakeRequest(t, req, http.StatusSeeOther)

	session := loginUser(t, "user2")
	req = NewRequest(t, "GET", "/deployments/insights")
	body := session.MakeRequest(t, req, http.StatusOK).Body.String()

	// The page itself carries no script: it mounts the bundled client and hands it the
	// namespace's own base and the server's own default window through window.config.pageData.
	// web_src/js/features/deployments/api.ts and InsightsPage.vue are what name /insights,
	// /insights/trends, /insights/repos and /runs, proven in routers/web/hubroutes.
	assert.Contains(t, body, `"deploymentsInsights":{"apiBase":"`+deploymentsv1.BasePath+`"`,
		"the handler hands the page the namespace's own base, so the page reaches the documented API and nothing else")
	assert.Contains(t, body, fmt.Sprintf(`"defaultWindowDays":%d`, deployments_service.DefaultWindowDays),
		"the page opens on the server's own default window")
	assert.Contains(t, body, `data-global-init="initDeploymentsInsights"`,
		"the page mounts the bundled client that is the API's actual client")
}
