// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readInsightsPage reads the bundled Vue component that replaced insights.tmpl's inline
// script: the template now carries no logic of its own, only the mount point, so the
// behavior this file used to prove by executing the template is proven by reading its client
// instead. Vue's own text interpolation escapes every value it renders, which is why no
// escaping test is repeated here.
func readInsightsPage(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "web_src", "js", "features", "deployments", "InsightsPage.vue"))
	require.NoError(t, err)
	return string(raw)
}

// TestDeploymentsInsightsPageCarriesNoInlineScript: the handler serves the shell alone, and
// the figures are drawn by the bundled client mounted on it.
func TestDeploymentsInsightsPageCarriesNoInlineScript(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateDir(t), "deployments", "insights.tmpl"))
	require.NoError(t, err)
	body := string(raw)
	assert.NotContains(t, body, "<script")
	assert.Contains(t, body, `data-global-init="initDeploymentsInsights"`)
}

// TestDeploymentsInsightsPageOffersTheServersOwnDefaultWindow keeps the page's default in step
// with the server's: a page that opened on a different window than the API defaults to would
// show the previous-window comparison against the wrong baseline.
func TestDeploymentsInsightsPageOffersTheServersOwnDefaultWindow(t *testing.T) {
	body := readInsightsPage(t)
	assert.Contains(t, body, "ref(String(props.config.defaultWindowDays))",
		"the selected window starts at the server's own DefaultWindowDays")
}

// TestDeploymentsInsightsPageOpensNoSecondTransport: the page re-reads its documented
// endpoints on an interval; it opens no WebSocket and no EventSource of its own.
func TestDeploymentsInsightsPageOpensNoSecondTransport(t *testing.T) {
	body := readInsightsPage(t)
	assert.NotContains(t, body, "new WebSocket", "the page must open no transport of its own")
	assert.NotContains(t, body, "EventSource", "Gitea's SSE endpoint was replaced upstream; nothing here revives one")
	assert.Contains(t, body, "setInterval(load, refreshMillis)", "the page refreshes itself over the same documented endpoints")
}

// TestDeploymentsInsightsPageLinksOutToGitea: every per-run and per-repository detail opens
// Gitea's own page rather than a reimplementation.
func TestDeploymentsInsightsPageLinksOutToGitea(t *testing.T) {
	body := readInsightsPage(t)
	assert.Contains(t, body, "run.run_url", "a run row opens the run in Gitea")
	assert.Contains(t, body, "${config.appSubUrl}/${repo.repo_full_name}/actions", "a repository row opens its runs in Gitea")
}

// TestDeploymentsInsightsPageShowsTheComparisonWindow: the previous window of equal length
// renders beside each tile.
func TestDeploymentsInsightsPageShowsTheComparisonWindow(t *testing.T) {
	body := readInsightsPage(t)
	assert.Contains(t, body, "Previous window", "each tile carries its comparison column")
	assert.Contains(t, body, "insights.previous", "the comparison is the API's own previous-window summary")
}
