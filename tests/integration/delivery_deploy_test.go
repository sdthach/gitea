// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	actions_model "gitea.dev/models/actions"
	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	"gitea.dev/models/delivery"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/timeutil"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	"gitea.dev/services/notify"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deployThroughTheNotifier drives one deploy the way a real one arrives: through Gitea's
// own notifier, which is the fork's single capture point (E2, E11). Nothing here writes the
// tables directly, so a hook that stopped firing would fail these tests.
func deployThroughTheNotifier(t *testing.T, repo *repo_model.Repository, sender *user_model.User, environment, tag string, runID, at int64) {
	t.Helper()
	// Every registered notifier sees the event, and Gitea's own webhook notifier
	// dereferences repo.Owner, so the caller has to have loaded it — the same contract
	// services/actions/notify.go meets in production.
	require.NoError(t, repo.LoadOwner(t.Context()))

	run := &actions_model.ActionRun{
		ID: runID, RepoID: repo.ID, WorkflowID: "deploy-" + environment + ".yaml",
		Ref: "refs/tags/" + tag, CommitSHA: "65f1bf27bc3bf70f64657658635e66094edbcb4d",
		Status: actions_model.StatusWaiting, Updated: timeutil.TimeStamp(at),
	}
	notify.WorkflowRunStatusUpdate(t.Context(), repo, sender, run)

	run.Status = actions_model.StatusSuccess
	run.Updated = timeutil.TimeStamp(at + 10)
	notify.WorkflowRunStatusUpdate(t.Context(), repo, sender, run)
}

// TestAPIDeliveryGridProjectsRepeatDeploys is SC 14 over the wire: v1.0 to qa, v1.1 to qa,
// v1.0 to qa again leaves v1.0 at `✔ ×2 now`, v1.1 at `✔`, and three rows in the table.
// An implementation that upserted per (release, environment) would fail every assertion.
func TestAPIDeliveryGridProjectsRepeatDeploys(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	deployThroughTheNotifier(t, repo, sender, "qa", "v1.0", 9001, 1000)
	deployThroughTheNotifier(t, repo, sender, "qa", "v1.1", 9002, 2000)
	deployThroughTheNotifier(t, repo, sender, "qa", "v1.0", 9003, 3000)

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/grid?repo_id=1").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)

	var rows []struct {
		RepoFullName string `json:"repo_full_name"`
		ReleaseTag   string `json:"release_tag"`
		Cells        []struct {
			Environment string `json:"environment"`
			State       string `json:"state"`
			Symbol      string `json:"symbol"`
			Successes   int    `json:"successes"`
			RunID       int64  `json:"run_id"`
		} `json:"cells"`
	}
	DecodeJSON(t, resp, &rows)
	require.NotEmpty(t, rows)

	byTag := map[string]map[string]string{}
	successes := map[string]int{}
	for _, row := range rows {
		assert.Equal(t, "user2/repo1", row.RepoFullName)
		require.NotEmpty(t, row.Cells, "every row carries one cell per environment, in configured order (E7)")
		byTag[row.ReleaseTag] = map[string]string{}
		columns := make([]string, 0, len(row.Cells))
		for _, cell := range row.Cells {
			columns = append(columns, cell.Environment)
			byTag[row.ReleaseTag][cell.Environment] = cell.Symbol
			if cell.Environment == "qa" {
				successes[row.ReleaseTag] = cell.Successes
			}
		}
		assert.Equal(t, []string{"dev", "qa", "uat", "staging", "prod"}, columns,
			"environment sequence is configuration; nothing in Gitea expresses it (E7)")
	}

	require.Contains(t, byTag, "v1.0")
	require.Contains(t, byTag, "v1.1")
	assert.Equal(t, "✔ ×2 now", byTag["v1.0"]["qa"], "v1.0 reached qa twice and is what qa is holding")
	assert.Equal(t, 2, successes["v1.0"])
	assert.Equal(t, "✔", byTag["v1.1"]["qa"], "v1.1 reached qa but no longer holds it")
	assert.Equal(t, 1, successes["v1.1"])
	assert.Equal(t, "·", byTag["v1.0"]["prod"], "nothing has reached prod")

	// SC 15: the deployments endpoint returns the rows the notifier wrote — three of them,
	// not one per (release, environment).
	req = NewRequest(t, "GET", deliveryv1.BasePath+"/deployments?environment=qa&sort_by=id&order=asc").AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusOK)
	var deployments []*delivery.Deployment
	DecodeJSON(t, resp, &deployments)
	require.Len(t, deployments, 3, "three deploys leave three rows (E3, SC 14)")
	assert.Equal(t, []string{"v1.0", "v1.1", "v1.0"},
		[]string{deployments[0].ReleaseTag, deployments[1].ReleaseTag, deployments[2].ReleaseTag})
	assert.Equal(t, []int64{9001, 9002, 9003},
		[]int64{deployments[0].RunID, deployments[1].RunID, deployments[2].RunID})
	assert.Empty(t, resp.Header().Get("X-Total-Count"),
		"a cursor-paged resource carries no total: counting a table receiving concurrent inserts answers a stale question (I6)")
}

// TestAPIDeliveryAuditIsReadOnlyOverTheAPI is SC 19 and SC 24: the audit resource publishes
// no write verb, so PATCH and DELETE are refused at the route as well as at the table (I11).
func TestAPIDeliveryAuditIsReadOnlyOverTheAPI(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	deployThroughTheNotifier(t, repo, sender, "prod", "v1.1", 9101, 5000)

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/audit?sort_by=id&order=asc").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	var events []*delivery.AuditEvent
	DecodeJSON(t, resp, &events)
	require.Len(t, events, 2, "each deploy writes requested and a terminal event (E5)")
	require.NotZero(t, events[0].ID)

	for _, method := range []string{"PATCH", "DELETE", "POST", "PUT"} {
		req = NewRequest(t, method, deliveryv1.BasePath+"/audit").AddTokenAuth(token)
		MakeRequest(t, req, http.StatusMethodNotAllowed)
	}

	// The table refuses it too, so the guarantee does not depend on the router alone.
	err := delivery.AppendAuditEvent(t.Context(), events[0])
	require.Error(t, err)
	assert.Contains(t, err.Error(), "append-only")
}

// TestAPIDeliveryAuditOutlivesTheUserItNames is SC 19: deleting the deploying user from
// Gitea leaves the audit still naming them, because actor_login is denormalized and the row
// resolves no foreign key.
func TestAPIDeliveryAuditOutlivesTheUserItNames(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	deployer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
	deployThroughTheNotifier(t, repo, deployer, "uat", "v1.1", 9201, 6000)

	// Removing the user row is the whole test: if the audit resolved the login through a
	// join, every row would go anonymous here.
	_, err := db.GetEngine(t.Context()).ID(deployer.ID).Delete(new(user_model.User))
	require.NoError(t, err)

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)
	req := NewRequest(t, "GET", deliveryv1.BasePath+"/audit?environment=uat").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)

	var events []*delivery.AuditEvent
	DecodeJSON(t, resp, &events)
	require.NotEmpty(t, events)
	for _, e := range events {
		assert.Equal(t, "user4", e.ActorLogin, "the audit still names who deployed (E5, SC 19)")
		assert.Equal(t, int64(4), e.ActorID)
	}
}

// TestAPIDeliveryCursorPagingVisitsEveryRowOnce covers I6 for the first two resources that
// use it. An offset traversal over an append-only table repeats and skips rows; a cursor
// traversal does not.
func TestAPIDeliveryCursorPagingVisitsEveryRowOnce(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	for i := range int64(3) {
		deployThroughTheNotifier(t, repo, sender, "dev", "v1.1", 9300+i, 7000+i*100)
	}

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	seen := map[int64]int{}
	cursor := ""
	for range 10 {
		url := deliveryv1.BasePath + "/audit?limit=2&sort_by=id&order=asc"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		req := NewRequest(t, "GET", url).AddTokenAuth(token)
		resp := MakeRequest(t, req, http.StatusOK)
		var page []*delivery.AuditEvent
		DecodeJSON(t, resp, &page)
		for _, e := range page {
			seen[e.ID]++
		}
		cursor = resp.Header().Get(deliveryv1.NextCursorHeader)
		if cursor == "" {
			break
		}
	}
	require.Len(t, seen, 6, "three deploys write two events each, and the traversal reached all six")
	for id, times := range seen {
		assert.Equal(t, 1, times, "row %d was returned exactly once", id)
	}

	// Deployments page by cursor too, under both the resource's natural sort and an
	// explicit one, so the cursor's position value is read from the column the traversal is
	// actually sorted on.
	for _, sort := range []string{"", "&sort_by=id&order=asc"} {
		t.Run("deployments cursor"+sort, func(t *testing.T) {
			seenRuns := map[int64]int{}
			cursor := ""
			for range 10 {
				url := deliveryv1.BasePath + "/deployments?limit=2" + sort
				if cursor != "" {
					url += "&cursor=" + cursor
				}
				req := NewRequest(t, "GET", url).AddTokenAuth(token)
				resp := MakeRequest(t, req, http.StatusOK)
				var page []*delivery.Deployment
				DecodeJSON(t, resp, &page)
				for _, d := range page {
					seenRuns[d.RunID]++
				}
				cursor = resp.Header().Get(deliveryv1.NextCursorHeader)
				if cursor == "" {
					break
				}
			}
			assert.Equal(t, map[int64]int{9300: 1, 9301: 1, 9302: 1}, seenRuns,
				"each deployment is returned exactly once across the traversal (I6)")
		})
	}

	// A cursor issued under one sort is refused under another rather than silently
	// skipping and repeating rows.
	req := NewRequest(t, "GET", deliveryv1.BasePath+"/audit?limit=2&sort_by=id&order=asc").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	issued := resp.Header().Get(deliveryv1.NextCursorHeader)
	require.NotEmpty(t, issued)

	req = NewRequest(t, "GET", deliveryv1.BasePath+"/audit?limit=2&sort_by=occurred_unix&order=desc&cursor="+issued).AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusBadRequest)
	var refusal struct {
		Code            string `json:"code"`
		SuggestedAction string `json:"suggested_action"`
	}
	DecodeJSON(t, resp, &refusal)
	assert.Equal(t, "cursor_sort_mismatch", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action (A21)")

	// Cursor and page are the only two forms, and a resource accepts exactly one (I8).
	req = NewRequest(t, "GET", deliveryv1.BasePath+"/audit?page=2").AddTokenAuth(token)
	MakeRequest(t, req, http.StatusBadRequest)
	req = NewRequest(t, "GET", deliveryv1.BasePath+"/releases?cursor=x").AddTokenAuth(token)
	MakeRequest(t, req, http.StatusNotFound)
}

// TestAPIDeliveryReleasesExpandDeployments covers I9's expansion over the two new resources
// and E6: releases are read from Gitea's own model at render time.
func TestAPIDeliveryReleasesExpandDeployments(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	sender := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	deployThroughTheNotifier(t, repo, sender, "staging", "v1.1", 9401, 8000)

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", deliveryv1.BasePath+"/repos/user2/repo1/releases?expand=deployments").AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)

	var releases []*deliveryv1.Release
	DecodeJSON(t, resp, &releases)
	require.NotEmpty(t, releases)
	assert.NotEmpty(t, resp.Header().Get("X-Total-Count"), "releases are finite and stable, so they page by page (I7)")

	found := false
	for _, r := range releases {
		assert.NotEqual(t, "draft-release", r.TagName, "a draft is not a row of the grid")
		if r.TagName == "v1.1" {
			found = true
			require.Len(t, r.Deployments, 1)
			assert.Equal(t, "staging", r.Deployments[0].Environment)
			assert.NotEmpty(t, r.Target, "the release's own commitish, which the deploy status is posted against (D2)")
		}
	}
	assert.True(t, found, "v1.1 is a release of user2/repo1")

	// Deeper or unwhitelisted expansion is refused, naming what is accepted (I9, I4).
	req = NewRequest(t, "GET", deliveryv1.BasePath+"/repos/user2/repo1/releases?expand=audit").AddTokenAuth(token)
	MakeRequest(t, req, http.StatusBadRequest)

	// deployments expands release and audit.
	req = NewRequest(t, "GET", deliveryv1.BasePath+"/deployments?expand=release,audit").AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusOK)
	var deployments []*deliveryv1.Deployment
	DecodeJSON(t, resp, &deployments)
	require.Len(t, deployments, 1)
	require.NotNil(t, deployments[0].Release)
	assert.Equal(t, "v1.1", deployments[0].Release.TagName)
	assert.Len(t, deployments[0].Audit, 2, "the run's own requested and terminal events")
}

// TestDeliveryGridPageIsAClientOfTheAPI is E18/I14 for the new page: the handler serves the
// shell and every figure arrives over the documented endpoint.
func TestDeliveryGridPageIsAClientOfTheAPI(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/delivery/grid")
	MakeRequest(t, req, http.StatusSeeOther)

	session := loginUser(t, "user2")
	req = NewRequest(t, "GET", "/delivery/grid")
	resp := session.MakeRequest(t, req, http.StatusOK)
	assert.Contains(t, resp.Body.String(), deliveryv1.BasePath+"/grid",
		"the page fetches its rows from the documented endpoint (E18, I14)")
}
