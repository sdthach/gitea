// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readInsightsPage reads the bundled Vue component that replaced insights.tmpl's inline script.
func readInsightsPage(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "web_src", "js", "features", "deployments", "InsightsPage.vue"))
	require.NoError(t, err)
	return string(raw)
}

// readDeploymentsFeature concatenates every source file the deployments bundle ships, so a
// check here is not defeated by code that answers it living in a sibling file rather than
// InsightsPage.vue itself.
func readDeploymentsFeature(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "web_src", "js", "features", "deployments")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".test.ts") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		out.Write(raw)
		out.WriteByte('\n')
	}
	return out.String()
}

// TestDeploymentsInsightsPageCarriesNoInlineScript: the handler serves the shell alone.
func TestDeploymentsInsightsPageCarriesNoInlineScript(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateDir(t), "deployments", "insights.tmpl"))
	require.NoError(t, err)
	body := string(raw)
	assert.NotContains(t, body, "<script")
	assert.Contains(t, body, `data-global-init="initDeploymentsInsights"`)
}

// TestDeploymentsInsightsPageReadsTheServersOwnDefaultWindow keeps the page's default in step
// with the server's: it reads defaultWindowDays from its own config rather than a value of its
// own, and tests/integration/deployments_insights_test.go proves the server hands it the right one.
func TestDeploymentsInsightsPageReadsTheServersOwnDefaultWindow(t *testing.T) {
	body := readInsightsPage(t)
	assert.Contains(t, body, "props.config.defaultWindowDays",
		"the selected window starts from the server's own default, not one hardcoded here")
}

// TestDeploymentsInsightsPageOpensNoSecondTransport: the page opens no WebSocket or EventSource
// of its own, scanning every file the deployments bundle ships rather than InsightsPage.vue
// alone, so the answer cannot move into a sibling file unnoticed.
func TestDeploymentsInsightsPageOpensNoSecondTransport(t *testing.T) {
	body := readDeploymentsFeature(t)
	assert.NotContains(t, body, "new WebSocket", "the page must open no transport of its own")
	assert.NotContains(t, body, "EventSource", "Gitea's SSE endpoint was replaced upstream; nothing here revives one")
	assert.Contains(t, body, "setInterval(load, refreshMillis)", "the page refreshes itself over the same documented endpoints")
}

// TestDeploymentsInsightsPageLinksOutToGitea: every per-run and per-repository detail opens
// Gitea's own page, scanning every file the deployments bundle ships rather than
// InsightsPage.vue alone, so the answer cannot move into a sibling file unnoticed.
func TestDeploymentsInsightsPageLinksOutToGitea(t *testing.T) {
	body := readDeploymentsFeature(t)
	assert.Contains(t, body, "run.run_url", "a run row opens the run in Gitea")
	assert.Contains(t, body, "${config.appSubUrl}/${repo.repo_full_name}/actions", "a repository row opens its runs in Gitea")
}

// TestDeploymentsInsightsPageShowsTheComparisonWindow: the previous window renders beside each tile.
func TestDeploymentsInsightsPageShowsTheComparisonWindow(t *testing.T) {
	body := readInsightsPage(t)
	assert.Contains(t, body, "Previous window", "each tile carries its comparison column")
	assert.Contains(t, body, "insights.previous", "the comparison is the API's own previous-window summary")
}
