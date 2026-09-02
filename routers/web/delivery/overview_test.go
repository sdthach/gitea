// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	htmltemplate "html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"

	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	delivery_service "gitea.dev/services/delivery"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderOverview executes the CI page through html/template, the renderer Gitea uses, so what
// the assertions below read is the page a browser would receive rather than its source.
func renderOverview(t *testing.T, data map[string]any) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(templateDir(t), "delivery", "overview.tmpl"))
	require.NoError(t, err)

	tmpl := htmltemplate.New("root").Funcs(htmltemplate.FuncMap(templateStubs()))
	_, err = tmpl.New("base/head").Parse("<html><body>")
	require.NoError(t, err)
	_, err = tmpl.New("base/footer").Parse("</body></html>")
	require.NoError(t, err)
	_, err = tmpl.New("overview").Parse(string(raw))
	require.NoError(t, err)

	var out strings.Builder
	require.NoError(t, tmpl.ExecuteTemplate(&out, "overview", data))
	return out.String()
}

// TestDeliveryCIPageEscapesItsData proves the values the handler interpolates arrive as
// quoted JavaScript strings rather than as raw source. Parsing alone would not catch this:
// contextual escaping happens at execution.
func TestDeliveryCIPageEscapesItsData(t *testing.T) {
	body := renderOverview(t, map[string]any{
		"Title":             "CI/CD overview",
		"DeliveryAPIBase":   deliveryv1.BasePath,
		"AppSubURL":         `";alert(1);//`,
		"DefaultWindowDays": delivery_service.DefaultWindowDays,
	})

	assert.Contains(t, body, `const base = "`+deliveryv1.BasePath+`";`)
	assert.NotContains(t, body, `const subURL = "";alert(1);//";`,
		"a sub-URL must not be able to close the string literal")
	assert.Contains(t, body, "alert", "the value is still rendered, just escaped")
}

// TestDeliveryCIPageOffersTheServersOwnDefaultWindow keeps the page's default in step with
// the server's: a page that opened on a different window than the API defaults to would show
// the previous-window comparison against the wrong baseline.
func TestDeliveryCIPageOffersTheServersOwnDefaultWindow(t *testing.T) {
	body := renderOverview(t, map[string]any{
		"Title":             "CI/CD overview",
		"DeliveryAPIBase":   deliveryv1.BasePath,
		"AppSubURL":         "",
		"DefaultWindowDays": delivery_service.DefaultWindowDays,
	})
	assert.Contains(t, body, `<option value="7" selected>`,
		"the selected window is the server's own DefaultWindowDays")
}

// TestDeliveryCIPageOpensNoSecondTransport asserts it as the repository permits. The
// page re-reads its documented endpoints on an interval; it opens no WebSocket and no
// EventSource of its own.
func TestDeliveryCIPageOpensNoSecondTransport(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateDir(t), "delivery", "overview.tmpl"))
	require.NoError(t, err)
	body := string(raw)

	assert.NotContains(t, body, "new WebSocket", "the page must open no transport of its own")
	assert.NotContains(t, body, "EventSource", "Gitea's SSE endpoint was replaced upstream; nothing here revives one")
	assert.Contains(t, body, "setInterval(load,", "the page refreshes itself over the same documented endpoints")
}

// TestDeliveryCIPageLinksOutToGitea: every per-run and per-repository detail opens
// Gitea's own page rather than a reimplementation.
func TestDeliveryCIPageLinksOutToGitea(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateDir(t), "delivery", "overview.tmpl"))
	require.NoError(t, err)
	body := string(raw)

	assert.Contains(t, body, "run.run_url", "a run row opens the run in Gitea")
	assert.Contains(t, body, "${subURL}/${r.repo_full_name}/actions", "a repository row opens its runs in Gitea")
}

// TestDeliveryCIPageShowsTheComparisonWindow: the previous window of equal length
// renders beside each tile.
func TestDeliveryCIPageShowsTheComparisonWindow(t *testing.T) {
	body := renderOverview(t, map[string]any{
		"Title":             "CI/CD overview",
		"DeliveryAPIBase":   deliveryv1.BasePath,
		"AppSubURL":         "",
		"DefaultWindowDays": delivery_service.DefaultWindowDays,
	})
	assert.Contains(t, body, "Previous window", "each tile carries its comparison column")
	assert.Contains(t, body, "overview.previous", "the comparison is the API's own previous-window summary")
}
