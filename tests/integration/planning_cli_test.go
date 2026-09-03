// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	planningclient "gitea.dev/cmd/gitea-planning/client"
	"gitea.dev/cmd/hubcli"
	auth_model "gitea.dev/models/auth"
	issues_model "gitea.dev/models/issues"
	planningv1 "gitea.dev/routers/api/planning/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/require"
)

// routerDoer drives the generated CLI straight into this test binary's own router, exactly as
// MakeRequest does, so the command set the built binary ships is what this test exercises —
// with no network round trip and no separate process to build or start.
type routerDoer struct{}

func (routerDoer) Do(req *http.Request) (*http.Response, error) {
	req.RequestURI = req.URL.Path
	if req.URL.RawQuery != "" {
		req.RequestURI += "?" + req.URL.RawQuery
	}
	if req.RemoteAddr == "" {
		req.RemoteAddr = "test-mock:12345"
	}
	rec := httptest.NewRecorder()
	testWebRoutes.ServeHTTP(rec, req)
	return rec.Result(), nil
}

// TestPlanningCLIMovesAnIssueToAMilestone drives gitea-planning's generated command set — the
// same table the built binary serves, from cmd/gitea-planning/client — against this test
// server, proving the whole request round trip works end to end: the CLI composes
// milestone_id as a JSON number, the handler decodes it, and Gitea's own service records the
// move. Reverting composeBody to string-marshal integers turns this red.
func TestPlanningCLIMovesAnIssueToAMilestone(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	previous := hubcli.Transport
	hubcli.Transport = routerDoer{}
	t.Cleanup(func() { hubcli.Transport = previous })

	cfg := hubcli.Config{
		Name:     "gitea-planning",
		BasePath: planningv1.BasePath,
		Commands: planningclient.Commands,
	}

	var stdout, stderr strings.Builder
	err := hubcli.Run(cfg, []string{
		"issue-move-milestone", "--repo", "user2/repo1", "--milestone-id", "1",
		"--server", "http://test-instance", "--token", token, "1",
	}, &stdout, &stderr)
	require.Nil(t, err, "stdout=%s stderr=%s", stdout.String(), stderr.String())

	issue, loadErr := issues_model.GetIssueByID(t.Context(), 1)
	require.NoError(t, loadErr)
	require.EqualValues(t, 1, issue.MilestoneID, "the CLI's issue-move-milestone recorded the move")
}
