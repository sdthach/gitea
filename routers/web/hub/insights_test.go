// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	htmltemplate "html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"

	deploymentsv1 "gitea.dev/routers/api/deployments/v1"
	deployments_service "gitea.dev/services/deployments"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderInsights executes the insights page through html/template, the renderer Gitea uses, so
// what the assertions below read is the page a browser would receive rather than its source.
func renderInsights(t *testing.T, data map[string]any) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(templateDir(t), "deployments", "insights.tmpl"))
	require.NoError(t, err)

	tmpl := htmltemplate.New("root").Funcs(htmltemplate.FuncMap(templateStubs()))
	_, err = tmpl.New("base/head").Parse("<html><body>")
	require.NoError(t, err)
	_, err = tmpl.New("base/footer").Parse("</body></html>")
	require.NoError(t, err)
	_, err = tmpl.New("insights").Parse(string(raw))
	require.NoError(t, err)

	var out strings.Builder
	require.NoError(t, tmpl.ExecuteTemplate(&out, "insights", data))
	return out.String()
}

// TestDeploymentsInsightsPageEscapesItsData proves the values the handler interpolates arrive
// as quoted JavaScript strings rather than as raw source. Parsing alone would not catch this:
// contextual escaping happens at execution.
func TestDeploymentsInsightsPageEscapesItsData(t *testing.T) {
	body := renderInsights(t, map[string]any{
		"Title":             "Insights",
		"APIBase":           deploymentsv1.BasePath,
		"AppSubURL":         `";alert(1);//`,
		"DefaultWindowDays": deployments_service.DefaultWindowDays,
	})

	assert.Contains(t, body, `const base = "`+deploymentsv1.BasePath+`";`)
	assert.NotContains(t, body, `const subURL = "";alert(1);//";`,
		"a sub-URL must not be able to close the string literal")
	assert.Contains(t, body, "alert", "the value is still rendered, just escaped")
}

// TestDeploymentsInsightsPageOffersTheServersOwnDefaultWindow keeps the page's default in step
// with the server's: a page that opened on a different window than the API defaults to would
// show the previous-window comparison against the wrong baseline.
func TestDeploymentsInsightsPageOffersTheServersOwnDefaultWindow(t *testing.T) {
	body := renderInsights(t, map[string]any{
		"Title":             "Insights",
		"APIBase":           deploymentsv1.BasePath,
		"AppSubURL":         "",
		"DefaultWindowDays": deployments_service.DefaultWindowDays,
	})
	assert.Contains(t, body, `<option value="7" selected>`,
		"the selected window is the server's own DefaultWindowDays")
}

// TestDeploymentsInsightsPageOpensNoSecondTransport asserts it as the repository permits. The
// page re-reads its documented endpoints on an interval; it opens no WebSocket and no
// EventSource of its own.
func TestDeploymentsInsightsPageOpensNoSecondTransport(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateDir(t), "deployments", "insights.tmpl"))
	require.NoError(t, err)
	body := string(raw)

	assert.NotContains(t, body, "new WebSocket", "the page must open no transport of its own")
	assert.NotContains(t, body, "EventSource", "Gitea's SSE endpoint was replaced upstream; nothing here revives one")
	assert.Contains(t, body, "setInterval(load,", "the page refreshes itself over the same documented endpoints")
}

// TestDeploymentsInsightsPageLinksOutToGitea: every per-run and per-repository detail opens
// Gitea's own page rather than a reimplementation.
func TestDeploymentsInsightsPageLinksOutToGitea(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateDir(t), "deployments", "insights.tmpl"))
	require.NoError(t, err)
	body := string(raw)

	assert.Contains(t, body, "run.run_url", "a run row opens the run in Gitea")
	assert.Contains(t, body, "${subURL}/${r.repo_full_name}/actions", "a repository row opens its runs in Gitea")
}

// TestDeploymentsInsightsPageShowsTheComparisonWindow: the previous window of equal length
// renders beside each tile.
func TestDeploymentsInsightsPageShowsTheComparisonWindow(t *testing.T) {
	body := renderInsights(t, map[string]any{
		"Title":             "Insights",
		"APIBase":           deploymentsv1.BasePath,
		"AppSubURL":         "",
		"DefaultWindowDays": deployments_service.DefaultWindowDays,
	})
	assert.Contains(t, body, "Previous window", "each tile carries its comparison column")
	assert.Contains(t, body, "overview.previous", "the comparison is the API's own previous-window summary")
}
