// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hubcli

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

// testConfig is a small fixture standing in for a real binary's generated Commands: one list
// command, one get-by-id command, and two command names sharing one create operation, the
// way deploy and rollback share one endpoint in the real CLIs.
func testConfig() Config {
	return Config{
		Name:          "gitea-hubcli-test",
		BasePath:      "/api/test/v1",
		DocPath:       "docs/test/openapi.json",
		TokenEnvVars:  []string{"HUBCLI_TEST_TOKEN", "FORGE_TOKEN", "GITEA_TOKEN"},
		ServerEnvVars: []string{"HUBCLI_TEST_SERVER", "GITEA_SERVER", "FORGE_HOST"},
		Commands: []Command{
			{
				Name: "widgets", OperationID: "listWidgets", Method: http.MethodGet, Path: "/widgets",
				Summary: "List widgets", QueryParams: []string{"color"}, Columns: []string{"id", "name"}, IsList: true,
			},
			{
				Name: "widget", OperationID: "getWidget", Method: http.MethodGet, Path: "/widgets/{id}",
				Summary: "Get a widget", PathParams: []string{"id"}, Columns: []string{"id", "name"}, IsList: false,
			},
			{
				Name: "create-widget", OperationID: "createWidget", Method: http.MethodPost, Path: "/widgets",
				Summary: "Create a widget", BodyParams: []string{"active", "color", "name"}, RequiredBody: []string{"name"},
				BoolBody: []string{"active"}, BodyHelp: map[string]string{"name": "widget name"}, Columns: []string{"id", "name"},
			},
			{
				Name: "reissue-widget", OperationID: "createWidget", Method: http.MethodPost, Path: "/widgets",
				Summary: "Create a widget (alias)", BodyParams: []string{"active", "color", "name"}, RequiredBody: []string{"name"},
				BoolBody: []string{"active"}, BodyHelp: map[string]string{"name": "widget name"}, Columns: []string{"id", "name"},
			},
			{
				Name: "resize-widget", OperationID: "resizeWidget", Method: http.MethodPut, Path: "/widgets/{id}/size",
				Summary: "Resize a widget", PathParams: []string{"id"}, BodyParams: []string{"size", "tags", "weight"},
				IntBody: []string{"size"}, FloatBody: []string{"weight"}, ArrayBody: []string{"tags"}, Columns: []string{"id", "name"},
			},
		},
	}
}

func withRecorder(t *testing.T, rec *recorder, cfg Config) {
	t.Helper()
	previous := Transport
	Transport = rec
	t.Cleanup(func() { Transport = previous })
	t.Setenv(cfg.ServerEnvVars[0], "https://gitea.example.invalid")
	t.Setenv(cfg.TokenEnvVars[0], "t0ken")
	t.Setenv("FORGE_TOKEN", "")
	t.Setenv("GITEA_TOKEN", "")
}

func exec(t *testing.T, cfg Config, rec *recorder, args ...string) (string, string, *Error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := Run(cfg, args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestListCommandComposesItsRequest(t *testing.T) {
	cfg := testConfig()
	rec := &recorder{body: "[]"}
	withRecorder(t, rec, cfg)

	_, _, err := exec(t, cfg, rec, "widgets")
	require.Nil(t, err)
	require.Len(t, rec.requests, 1)
	assert.Equal(t, http.MethodGet, rec.requests[0].Method)
	assert.Equal(t, "/api/test/v1/widgets", rec.requests[0].URL.Path)
	assert.Equal(t, "token t0ken", rec.requests[0].Header.Get("Authorization"))
}

func TestGetCommandFillsThePositionalIntoThePath(t *testing.T) {
	cfg := testConfig()
	rec := &recorder{body: `{"id":7,"name":"cog"}`}
	withRecorder(t, rec, cfg)

	_, _, err := exec(t, cfg, rec, "widget", "7")
	require.Nil(t, err)
	require.Len(t, rec.requests, 1)
	assert.Equal(t, "/api/test/v1/widgets/7", rec.requests[0].URL.Path)
}

// TestFlagsBecomeQueryParameters: the CLI does not filter client-side.
func TestFlagsBecomeQueryParameters(t *testing.T) {
	cfg := testConfig()
	rec := &recorder{body: "[]"}
	withRecorder(t, rec, cfg)

	_, _, err := exec(t, cfg, rec, "widgets",
		"--filter", "color=red",
		"-q", "cog",
		"--sort-by", "name",
		"--order", "desc",
		"--limit", "20",
		"--offset", "40")
	require.Nil(t, err)
	require.Len(t, rec.requests, 1)

	q := rec.requests[0].URL.Query()
	assert.Equal(t, "red", q.Get("color"))
	assert.Equal(t, "cog", q.Get("q"))
	assert.Equal(t, "name", q.Get("sort_by"))
	assert.Equal(t, "desc", q.Get("order"))
	assert.Equal(t, "20", q.Get("limit"))
	assert.Equal(t, "3", q.Get("page"), "--offset 40 at --limit 20 is the third 1-based page")
}

func TestRepeatedFilterOnOneFieldIsSentTwice(t *testing.T) {
	cfg := testConfig()
	rec := &recorder{body: "[]"}
	withRecorder(t, rec, cfg)

	_, _, err := exec(t, cfg, rec, "widgets", "--filter", "color[gte]=10", "--filter", "color[lte]=40")
	require.Nil(t, err)
	q := rec.requests[0].URL.Query()
	assert.Equal(t, []string{"10"}, q["color[gte]"])
	assert.Equal(t, []string{"40"}, q["color[lte]"])
}

func TestJSONIsVerbatim(t *testing.T) {
	cfg := testConfig()
	payload := `[{"id":1,"name":"cog"}]`
	rec := &recorder{body: payload}
	withRecorder(t, rec, cfg)

	stdout, _, err := exec(t, cfg, rec, "widgets", "--json")
	require.Nil(t, err)
	assert.Equal(t, payload+"\n", stdout, "--json emits the API response unshaped")
}

func TestTableIsTheDefault(t *testing.T) {
	cfg := testConfig()
	rec := &recorder{body: `[{"id":1,"name":"cog"}]`}
	withRecorder(t, rec, cfg)

	stdout, _, err := exec(t, cfg, rec, "widgets")
	require.Nil(t, err)
	assert.Contains(t, stdout, "NAME")
	assert.Contains(t, stdout, "cog")
}

// TestServerRejectionSurfacesItsSuggestedAction: the server's suggested next action
// survives the CLI boundary.
func TestServerRejectionSurfacesItsSuggestedAction(t *testing.T) {
	cfg := testConfig()
	rec := &recorder{
		status: http.StatusBadRequest,
		body:   `{"code":"unknown_filter_field","message":"\"colour\" is not a filterable field of \"widgets\"","suggested_action":"Remove \"colour\".","accepted":["id","name"]}`,
	}
	withRecorder(t, rec, cfg)

	_, _, err := exec(t, cfg, rec, "widgets", "--filter", "colour=red")
	require.NotNil(t, err)
	assert.Equal(t, 1, err.ExitCode)
	assert.Contains(t, err.Message, "colour")
	assert.Contains(t, err.SuggestedAction, "Remove")
	assert.Contains(t, err.SuggestedAction, "id, name", "the accepted set reaches the operator")
}

func TestRefusalExitsThree(t *testing.T) {
	cfg := testConfig()
	rec := &recorder{status: http.StatusForbidden, body: `{"code":"forbidden","message":"no","suggested_action":"Ask an admin."}`}
	withRecorder(t, rec, cfg)

	_, _, err := exec(t, cfg, rec, "widgets")
	require.NotNil(t, err)
	assert.Equal(t, 3, err.ExitCode, "a refusal is distinguishable from a failed request")
}

func TestUsageErrors(t *testing.T) {
	cfg := testConfig()
	rec := &recorder{body: "[]"}
	withRecorder(t, rec, cfg)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown command", []string{"no-such-command"}, "unknown command"},
		{"missing positional", []string{"widget"}, "positional"},
		{"filter with no equals", []string{"widgets", "--filter", "broken"}, "no '='"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := exec(t, cfg, rec, c.args...)
			require.NotNil(t, err)
			assert.Equal(t, 2, err.ExitCode)
			assert.Contains(t, err.Message, c.want)
			assert.NotEmpty(t, err.SuggestedAction, "every error carries a suggested next action")
		})
	}
}

func TestMissingCredentialsNameWhatToSet(t *testing.T) {
	cfg := testConfig()
	previous := Transport
	Transport = &recorder{body: "[]"}
	t.Cleanup(func() { Transport = previous })
	for _, name := range append(append([]string{}, cfg.TokenEnvVars...), cfg.ServerEnvVars...) {
		t.Setenv(name, "")
	}

	_, _, err := exec(t, cfg, nil, "widgets")
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "server")
	assert.Contains(t, err.SuggestedAction, "--server")

	t.Setenv(cfg.ServerEnvVars[0], "https://gitea.example.invalid")
	_, _, err = exec(t, cfg, nil, "widgets")
	require.NotNil(t, err)
	assert.Contains(t, err.Message, "token")
	assert.Contains(t, err.SuggestedAction, "user/settings/applications")
}

func TestListOperationsIsStable(t *testing.T) {
	cfg := testConfig()
	var stdout bytes.Buffer
	require.Nil(t, Run(cfg, []string{"--list-operations"}, &stdout, io.Discard))
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	assert.Len(t, lines, len(cfg.Commands))
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
	cfg := testConfig()
	for _, args := range [][]string{{}, {"--help"}, {"-h"}, {"help"}} {
		var stdout bytes.Buffer
		require.Nil(t, Run(cfg, args, &stdout, io.Discard))
		body := stdout.String()
		for _, cmd := range cfg.Commands {
			assert.Contains(t, body, cmd.Name, "help must list every command")
			assert.Contains(t, body, cmd.Summary)
		}
		assert.Contains(t, body, "--json")
		assert.Contains(t, body, "--filter")
		assert.Contains(t, body, "Exit codes:", "the help states what each exit code means")
		for _, source := range cfg.TokenEnvVars {
			assert.Contains(t, body, source, "help names every credential source it consults")
		}
	}
}

// TestSubcommandHelpGoesToStdoutAndExitsZero: --help is a request for documentation, not a
// usage error, and the generated reference depends on both.
func TestSubcommandHelpGoesToStdoutAndExitsZero(t *testing.T) {
	cfg := testConfig()
	for _, cmd := range cfg.Commands {
		t.Run(cmd.Name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			require.Nil(t, Run(cfg, []string{cmd.Name, "--help"}, &stdout, &stderr))
			assert.Contains(t, stdout.String(), cmd.Summary)
			assert.Contains(t, stdout.String(), cmd.Path)
			assert.Empty(t, stderr.String(), "help is not an error, so nothing goes to stderr")
		})
	}
}

// requestBody reads back what the CLI composed. http.NewRequest over a bytes.Reader sets
// GetBody, so the recorder can be asked for the payload after the fact.
func requestBody(t *testing.T, req *http.Request) string {
	t.Helper()
	if req.GetBody == nil {
		return ""
	}
	rc, err := req.GetBody()
	require.NoError(t, err)
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	require.NoError(t, err)
	return string(raw)
}

// TestTwoCommandNamesOnOneOperationComposeTheIdenticalRequest: deploy and rollback in the
// real CLIs are the same endpoint under two names, composing byte-identical requests at the
// same inputs. This is the same mechanism, generically.
func TestTwoCommandNamesOnOneOperationComposeTheIdenticalRequest(t *testing.T) {
	cfg := testConfig()
	args := func(command string) []string {
		return []string{command, "--name", "cog", "--color", "red", "--active"}
	}

	rec := &recorder{body: "{}", status: http.StatusCreated}
	withRecorder(t, rec, cfg)
	_, _, err := exec(t, cfg, rec, args("create-widget")...)
	require.Nil(t, err)

	alias := &recorder{body: "{}", status: http.StatusCreated}
	withRecorder(t, alias, cfg)
	_, _, err = exec(t, cfg, alias, args("reissue-widget")...)
	require.Nil(t, err)

	require.Len(t, rec.requests, 1)
	require.Len(t, alias.requests, 1)
	assert.Equal(t, rec.requests[0].URL.String(), alias.requests[0].URL.String())
	assert.JSONEq(t, requestBody(t, rec.requests[0]), requestBody(t, alias.requests[0]))
	assert.JSONEq(t, `{"active":true,"color":"red","name":"cog"}`, requestBody(t, rec.requests[0]))
	// The bytes are deterministic, not merely equivalent: members are written in the order
	// the generated command lists them, which is sorted.
	assert.True(t, strings.HasPrefix(requestBody(t, rec.requests[0]), `{"active":`),
		"body members are written in sorted order")
}

// TestUnsetSwitchIsOmitted: an unset bool flag is left out of the body, so the endpoint's
// own default stands rather than an explicit false overriding it.
func TestUnsetSwitchIsOmitted(t *testing.T) {
	cfg := testConfig()
	rec := &recorder{body: "{}", status: http.StatusCreated}
	withRecorder(t, rec, cfg)

	_, _, err := exec(t, cfg, rec, "create-widget", "--name", "cog")
	require.Nil(t, err)
	assert.JSONEq(t, `{"name":"cog"}`, requestBody(t, rec.requests[0]))
}

// TestMissingRequiredBodyIsRefusedBeforeTheRoundTrip: a request the endpoint would refuse is
// refused before the round-trip, naming what is missing and how to call it.
func TestMissingRequiredBodyIsRefusedBeforeTheRoundTrip(t *testing.T) {
	cfg := testConfig()
	rec := &recorder{body: "{}"}
	withRecorder(t, rec, cfg)

	_, _, err := exec(t, cfg, rec, "create-widget", "--color", "red")
	require.NotNil(t, err)
	assert.Equal(t, 2, err.ExitCode)
	assert.Contains(t, err.Message, "--name")
	assert.NotEmpty(t, err.SuggestedAction)
	assert.Empty(t, rec.requests, "nothing was sent")
}

// TestIntBodyMarshalsAsANumber: an IntBody member is sent as a JSON number, not the string
// every other body member marshals as — the handler decodes it into an int64 field.
func TestIntBodyMarshalsAsANumber(t *testing.T) {
	cfg := testConfig()
	rec := &recorder{body: "{}"}
	withRecorder(t, rec, cfg)

	_, _, err := exec(t, cfg, rec, "resize-widget", "--size", "3", "7")
	require.Nil(t, err)
	assert.JSONEq(t, `{"size":3}`, requestBody(t, rec.requests[0]))
}

// TestIntBodyRefusesANonInteger: a non-integer value is refused before the round-trip, naming
// the flag.
func TestIntBodyRefusesANonInteger(t *testing.T) {
	cfg := testConfig()
	rec := &recorder{body: "{}"}
	withRecorder(t, rec, cfg)

	_, _, err := exec(t, cfg, rec, "resize-widget", "--size", "not-a-number", "7")
	require.NotNil(t, err)
	assert.Equal(t, 2, err.ExitCode)
	assert.Contains(t, err.Message, "--size")
	assert.Empty(t, rec.requests, "nothing was sent")
}

// TestFloatBodyMarshalsAsANumber: a FloatBody member is sent as a JSON number, not the string
// every other body member marshals as — the handler decodes it into a float64 field.
func TestFloatBodyMarshalsAsANumber(t *testing.T) {
	cfg := testConfig()
	rec := &recorder{body: "{}"}
	withRecorder(t, rec, cfg)

	_, _, err := exec(t, cfg, rec, "resize-widget", "--weight", "6.5", "7")
	require.Nil(t, err)
	assert.JSONEq(t, `{"weight":6.5}`, requestBody(t, rec.requests[0]))
}

// TestFloatBodyRefusesANonNumber: a non-numeric value is refused before the round-trip, naming
// the flag.
func TestFloatBodyRefusesANonNumber(t *testing.T) {
	cfg := testConfig()
	rec := &recorder{body: "{}"}
	withRecorder(t, rec, cfg)

	_, _, err := exec(t, cfg, rec, "resize-widget", "--weight", "not-a-number", "7")
	require.NotNil(t, err)
	assert.Equal(t, 2, err.ExitCode)
	assert.Contains(t, err.Message, "--weight")
	assert.Empty(t, rec.requests, "nothing was sent")
}

// TestArrayBodyAcceptsAJSONArrayOrACommaSeparatedList: an ArrayBody flag's value is a JSON
// array when it starts with '[', otherwise a comma-separated list of strings.
func TestArrayBodyAcceptsAJSONArrayOrACommaSeparatedList(t *testing.T) {
	cfg := testConfig()

	rec := &recorder{body: "{}"}
	withRecorder(t, rec, cfg)
	_, _, err := exec(t, cfg, rec, "resize-widget", "--tags", `["red","blue"]`, "7")
	require.Nil(t, err)
	assert.JSONEq(t, `{"tags":["red","blue"]}`, requestBody(t, rec.requests[0]))

	rec2 := &recorder{body: "{}"}
	withRecorder(t, rec2, cfg)
	_, _, err = exec(t, cfg, rec2, "resize-widget", "--tags", "red, blue", "7")
	require.Nil(t, err)
	assert.JSONEq(t, `{"tags":["red","blue"]}`, requestBody(t, rec2.requests[0]))
}
