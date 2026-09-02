// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorder is the recorded API every CLI test runs against. No test reaches a live server:
// each asserts the request the CLI composed and the exit code it returned.
type recorder struct {
	requests []*http.Request
	status   int
	body     string
}

func (r *recorder) Do(req *http.Request) (*http.Response, error) {
	r.requests = append(r.requests, req)
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func withRecorder(t *testing.T, rec *recorder) {
	t.Helper()
	previous := Transport
	Transport = rec
	t.Cleanup(func() { Transport = previous })
	t.Setenv("GITEA_DELIVERY_SERVER", "https://gitea.example.invalid")
	t.Setenv("GITEA_DELIVERY_TOKEN", "t0ken")
	t.Setenv("FORGE_TOKEN", "")
	t.Setenv("GITEA_TOKEN", "")
}

func exec(t *testing.T, rec *recorder, args ...string) (string, string, *Error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// TestEveryCommandComposesItsRequest is one test per command.
func TestEveryCommandComposesItsRequest(t *testing.T) {
	cases := map[string]struct {
		args     []string
		wantPath string
		// wantMethod is empty for the GET commands, which is every list and read endpoint.
		wantMethod string
	}{
		"environments":             {[]string{"environments"}, "/api/delivery/v1/environments", ""},
		"repos":                    {[]string{"repos"}, "/api/delivery/v1/repos", ""},
		"repo-environments":        {[]string{"repo-environments", "acme", "web"}, "/api/delivery/v1/repos/acme/web/environments", ""},
		"repo-environment":         {[]string{"repo-environment", "acme", "web", "prod"}, "/api/delivery/v1/repos/acme/web/environments/prod", ""},
		"repo-environment-secrets": {[]string{"repo-environment-secrets", "acme", "web", "prod"}, "/api/delivery/v1/repos/acme/web/environments/prod/secrets", ""},
		"deployments":              {[]string{"deployments"}, "/api/delivery/v1/deployments", ""},
		"audit":                    {[]string{"audit"}, "/api/delivery/v1/audit", ""},
		"releases":                 {[]string{"releases", "acme", "web"}, "/api/delivery/v1/repos/acme/web/releases", ""},
		"grid":                     {[]string{"grid"}, "/api/delivery/v1/grid", ""},
		// deploy and rollback are one endpoint under two names, because rolling
		// back is deploying a prior release tag rather than a second path.
		"deploy": {
			[]string{"deploy", "--repo", "acme/web", "--environment", "prod", "--release-tag", "v1.0"},
			"/api/delivery/v1/deployments", http.MethodPost,
		},
		"rollback": {
			[]string{"rollback", "--repo", "acme/web", "--environment", "prod", "--release-tag", "v0.9"},
			"/api/delivery/v1/deployments", http.MethodPost,
		},
		// the cross-repository CI overview's resources.
		"runs":            {[]string{"runs"}, "/api/delivery/v1/runs", ""},
		"workflows":       {[]string{"workflows"}, "/api/delivery/v1/workflows", ""},
		"overview":        {[]string{"overview"}, "/api/delivery/v1/overview", ""},
		"overview-trends": {[]string{"overview-trends"}, "/api/delivery/v1/overview/trends", ""},
		"overview-repos":  {[]string{"overview-repos"}, "/api/delivery/v1/overview/repos", ""},
		// a gated environment holds a CLI-started deploy identically, and these
		// are the only way to release it — there is no CLI path around the gate.
		"approvals": {[]string{"approvals"}, "/api/delivery/v1/approvals", ""},
		"approve":   {[]string{"approve", "42"}, "/api/delivery/v1/approvals/42/approve", http.MethodPost},
		"reject":    {[]string{"reject", "42"}, "/api/delivery/v1/approvals/42/reject", http.MethodPost},
		// the board with its swimlanes, its two writes, and the timeline, which
		// needs no Projects API and so is reachable where the board is not.
		"board": {
			[]string{"board", "--filter", "repo_id=1", "--filter", "project_id=5", "--filter", "group_by=type"},
			"/api/delivery/v1/board", "",
		},
		"timeline": {[]string{"timeline", "--filter", "repo_id=1"}, "/api/delivery/v1/timeline", ""},
		"board-move-column": {
			[]string{"board-move-column", "--repo", "acme/web", "--project-id", "5", "--column-id", "12", "9042"},
			"/api/delivery/v1/board/cards/9042/column", http.MethodPost,
		},
		"board-move-lane": {
			[]string{"board-move-lane", "--repo", "acme/web", "--project-id", "5", "--group-by", "type", "--lane", "bug", "9042"},
			"/api/delivery/v1/board/cards/9042/lane", http.MethodPost,
		},
		"environment-create": {
			[]string{"environment-create", "--name", "staging"},
			"/api/delivery/v1/environments", http.MethodPost,
		},
		"environment-update": {
			[]string{"environment-update", "--name", "staging", "7"},
			"/api/delivery/v1/environments/7", http.MethodPut,
		},
		"environment-delete": {
			[]string{"environment-delete", "7"},
			"/api/delivery/v1/environments/7", http.MethodDelete,
		},
		"secret-scope-create": {
			[]string{"secret-scope-create", "--repo-id", "1", "--secret-name", "DEPLOY_KEY", "--environment", "prod"},
			"/api/delivery/v1/secret-scopes", http.MethodPost,
		},
		"secret-scope-delete": {
			[]string{"secret-scope-delete", "3"},
			"/api/delivery/v1/secret-scopes/3", http.MethodDelete,
		},
		"deployment-summary": {[]string{"deployment-summary"}, "/api/delivery/v1/deployment-summary", ""},
		"timeline-move-milestone": {
			[]string{"timeline-move-milestone", "--repo", "acme/widgets", "--milestone-id", "2", "9042"},
			"/api/delivery/v1/timeline/issues/9042/milestone", http.MethodPost,
		},
		"timeline-move-lane": {
			[]string{"timeline-move-lane", "--repo", "acme/widgets", "--group-by", "type", "--lane", "bug", "9042"},
			"/api/delivery/v1/timeline/issues/9042/lane", http.MethodPost,
		},
		"timeline-set-dates": {
			[]string{"timeline-set-dates", "--repo", "acme/widgets", "--start", "2026-03-01", "9042"},
			"/api/delivery/v1/timeline/issues/9042/dates", http.MethodPost,
		},
		"timeline-create-milestone": {
			[]string{"timeline-create-milestone", "--repo", "acme/widgets", "--title", "Sprint 9"},
			"/api/delivery/v1/timeline/milestones", http.MethodPost,
		},
		"timeline-create-issue": {
			[]string{"timeline-create-issue", "--repo", "acme/widgets", "--title", "Wire it"},
			"/api/delivery/v1/timeline/issues", http.MethodPost,
		},
		"environment": {[]string{"environment", "7"}, "/api/delivery/v1/environments/7", ""},
	}
	require.Len(t, cases, len(Commands), "every command needs a test; add one when an endpoint is added")

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cmd, ok := lookup(name)
			require.True(t, ok)
			body := "[]"
			if !cmd.IsList {
				body = "{}"
			}
			rec := &recorder{body: body}
			withRecorder(t, rec)

			_, _, err := exec(t, rec, c.args...)
			require.Nil(t, err)
			require.Len(t, rec.requests, 1)
			req := rec.requests[0]
			wantMethod := c.wantMethod
			if wantMethod == "" {
				wantMethod = http.MethodGet
			}
			assert.Equal(t, wantMethod, req.Method)
			assert.Equal(t, c.wantPath, req.URL.Path)
			assert.Equal(t, "token t0ken", req.Header.Get("Authorization"))
		})
	}
}

// TestFlagsBecomeQueryParameters: the CLI does not filter client-side.
func TestFlagsBecomeQueryParameters(t *testing.T) {
	rec := &recorder{body: "[]"}
	withRecorder(t, rec)

	_, _, err := exec(t, rec, "environments",
		"--filter", "sort_order[gte]=20",
		"--filter", "approval_policy=none",
		"-q", "prod",
		"--sort-by", "name",
		"--order", "desc",
		"--limit", "20",
		"--offset", "40")
	require.Nil(t, err)
	require.Len(t, rec.requests, 1)

	q := rec.requests[0].URL.Query()
	assert.Equal(t, "20", q.Get("sort_order[gte]"))
	assert.Equal(t, "none", q.Get("approval_policy"))
	assert.Equal(t, "prod", q.Get("q"))
	assert.Equal(t, "name", q.Get("sort_by"))
	assert.Equal(t, "desc", q.Get("order"))
	assert.Equal(t, "20", q.Get("limit"))
	assert.Equal(t, "3", q.Get("page"), "--offset 40 at --limit 20 is the third 1-based page")
}

func TestRepeatedFilterOnOneFieldIsSentTwice(t *testing.T) {
	rec := &recorder{body: "[]"}
	withRecorder(t, rec)

	_, _, err := exec(t, rec, "environments", "--filter", "sort_order[gte]=10", "--filter", "sort_order[lte]=40")
	require.Nil(t, err)
	q := rec.requests[0].URL.Query()
	assert.Equal(t, []string{"10"}, q["sort_order[gte]"])
	assert.Equal(t, []string{"40"}, q["sort_order[lte]"])
}

func TestJSONIsVerbatim(t *testing.T) {
	payload := `[{"id":1,"repo_id":0,"name":"prod","sort_order":50,"approval_policy":"none","required_approvals":1}]`
	rec := &recorder{body: payload}
	withRecorder(t, rec)

	stdout, _, err := exec(t, rec, "environments", "--json")
	require.Nil(t, err)
	assert.Equal(t, payload+"\n", stdout, "--json emits the API response unshaped")
}

func TestTableIsTheDefault(t *testing.T) {
	rec := &recorder{body: `[{"id":1,"repo_id":0,"name":"prod","sort_order":50,"approval_policy":"none","required_approvals":1}]`}
	withRecorder(t, rec)

	stdout, _, err := exec(t, rec, "environments")
	require.Nil(t, err)
	assert.Contains(t, stdout, "NAME")
	assert.Contains(t, stdout, "prod")
	assert.Contains(t, stdout, "APPROVAL_POLICY")
}

// TestServerRejectionSurfacesItsSuggestedAction: the server's suggested next action
// survives the CLI boundary.
func TestServerRejectionSurfacesItsSuggestedAction(t *testing.T) {
	rec := &recorder{
		status: http.StatusBadRequest,
		body:   `{"code":"unknown_filter_field","message":"\"colour\" is not a filterable field of \"environments\"","suggested_action":"Remove \"colour\".","accepted":["id","name"]}`,
	}
	withRecorder(t, rec)

	_, _, err := exec(t, rec, "environments", "--filter", "colour=red")
	require.NotNil(t, err)
	assert.Equal(t, 1, err.ExitCode)
	assert.Contains(t, err.Message, "colour")
	assert.Contains(t, err.SuggestedAction, "Remove")
	assert.Contains(t, err.SuggestedAction, "id, name", "the accepted set reaches the operator")
}

func TestRefusalExitsThree(t *testing.T) {
	rec := &recorder{status: http.StatusForbidden, body: `{"code":"forbidden","message":"no","suggested_action":"Ask an admin."}`}
	withRecorder(t, rec)

	_, _, err := exec(t, rec, "environments")
	require.NotNil(t, err)
	assert.Equal(t, 3, err.ExitCode, "a refusal is distinguishable from a failed request")
}

func TestUsageErrors(t *testing.T) {
	rec := &recorder{body: "[]"}
	withRecorder(t, rec)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown command", []string{"no-such-command"}, "unknown command"},
		{"missing positional", []string{"repo-environment", "acme"}, "positional"},
		{"filter with no equals", []string{"environments", "--filter", "broken"}, "no '='"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := exec(t, rec, c.args...)
			require.NotNil(t, err)
			assert.Equal(t, 2, err.ExitCode)
			assert.Contains(t, err.Message, c.want)
			assert.NotEmpty(t, err.SuggestedAction, "every error carries a suggested next action")
		})
	}
}

func TestMissingCredentialsNameWhatToSet(t *testing.T) {
	previous := Transport
	Transport = &recorder{body: "[]"}
	t.Cleanup(func() { Transport = previous })
	for _, name := range append(append([]string{}, tokenSources...), serverSources...) {
		t.Setenv(name, "")
	}

	_, _, err := exec(t, nil, "environments")
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "server")
	assert.Contains(t, err.SuggestedAction, "--server")

	t.Setenv("GITEA_DELIVERY_SERVER", "https://gitea.example.invalid")
	_, _, err = exec(t, nil, "environments")
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "token")
	assert.Contains(t, err.SuggestedAction, "user/settings/applications")
}

// TestCredentialPrecedence: one token serves the adapter and the CLI.
func TestCredentialPrecedence(t *testing.T) {
	env := func(pairs map[string]string) func(string) (string, bool) {
		return func(name string) (string, bool) { v, ok := pairs[name]; return v, ok }
	}
	cases := []struct {
		name       string
		flag       string
		env        map[string]string
		wantValue  string
		wantSource string
	}{
		{"flag wins", "flagged", map[string]string{"GITEA_DELIVERY_TOKEN": "a"}, "flagged", "--token"},
		{"delivery variable", "", map[string]string{"GITEA_DELIVERY_TOKEN": "a", "FORGE_TOKEN": "b"}, "a", "$GITEA_DELIVERY_TOKEN"},
		{"forge variable", "", map[string]string{"FORGE_TOKEN": "b", "GITEA_TOKEN": "c"}, "b", "$FORGE_TOKEN"},
		{"gitea variable", "", map[string]string{"GITEA_TOKEN": "c"}, "c", "$GITEA_TOKEN"},
		{"empty is not set", "", map[string]string{"GITEA_DELIVERY_TOKEN": "", "GITEA_TOKEN": "c"}, "c", "$GITEA_TOKEN"},
		{"nothing", "", nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			value, source := ResolveToken(c.flag, env(c.env))
			assert.Equal(t, c.wantValue, value)
			assert.Equal(t, c.wantSource, source, "the CLI must be able to report which credential source won")
		})
	}
}

func TestResolveServerTrimsTrailingSlash(t *testing.T) {
	value, source := ResolveServer("https://gitea.example.invalid/", func(string) (string, bool) { return "", false })
	assert.Equal(t, "https://gitea.example.invalid", value)
	assert.Equal(t, "--server", source)
}

func TestListOperationsIsStable(t *testing.T) {
	var stdout bytes.Buffer
	require.Nil(t, run([]string{"--list-operations"}, &stdout, io.Discard))
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	assert.Len(t, lines, len(Commands))
	for _, line := range lines {
		id, name, found := strings.Cut(line, "\t")
		require.True(t, found)
		assert.NotEmpty(t, id)
		assert.NotEmpty(t, name)
	}
}

// TestUsageListsEveryCommand covers the root --help, which is also the source the generated
// command reference is rendered from.
func TestUsageListsEveryCommand(t *testing.T) {
	for _, args := range [][]string{{}, {"--help"}, {"-h"}, {"help"}} {
		var stdout bytes.Buffer
		require.Nil(t, run(args, &stdout, io.Discard))
		body := stdout.String()
		for _, cmd := range Commands {
			assert.Contains(t, body, cmd.Name, "help must list every command")
			assert.Contains(t, body, cmd.Summary)
		}
		assert.Contains(t, body, "--json")
		assert.Contains(t, body, "--filter")
		assert.Contains(t, body, "Exit codes:", "the help states what each exit code means")
		for _, source := range tokenSources {
			assert.Contains(t, body, source, "help names every credential source it consults")
		}
	}
}

// TestSubcommandHelpGoesToStdoutAndExitsZero: --help is a request for documentation, not a
// usage error, and the generated reference depends on both.
func TestSubcommandHelpGoesToStdoutAndExitsZero(t *testing.T) {
	for _, cmd := range Commands {
		t.Run(cmd.Name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			require.Nil(t, run([]string{cmd.Name, "--help"}, &stdout, &stderr))
			assert.Contains(t, stdout.String(), cmd.Summary)
			assert.Contains(t, stdout.String(), cmd.Path)
			assert.Empty(t, stderr.String(), "help is not an error, so nothing goes to stderr")
		})
	}
}

// TestDeliveryRunsFilterComposesTheFailedRunsRequest is the CLI half: the command has to
// compose exactly the request the page's failed-runs list makes, or the two would disagree
// about what "failed" means.
func TestDeliveryRunsFilterComposesTheFailedRunsRequest(t *testing.T) {
	rec := &recorder{body: "[]"}
	withRecorder(t, rec)

	stdout, _, err := exec(t, rec, "runs", "--filter", "status[eq]=failure", "--json")
	require.Nil(t, err)
	require.Len(t, rec.requests, 1)

	req := rec.requests[0]
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/api/delivery/v1/runs", req.URL.Path)
	assert.Equal(t, "failure", req.URL.Query().Get("status[eq]"),
		"the filter is sent verbatim; the server maps the state name onto its own status integer")
	assert.Equal(t, "[]", strings.TrimSpace(stdout), "--json emits the API response verbatim and unshaped")
}
