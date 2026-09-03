// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hubroutes

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	deploymentsv1 "gitea.dev/routers/api/deployments/v1"
	hubapi "gitea.dev/routers/api/hub"
	planningv1 "gitea.dev/routers/api/planning/v1"
	hub_web "gitea.dev/routers/web/hub"
	"gitea.dev/services/context"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoRoot walks up to the repository root so the test reads the real template files.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find the repository root")
	return ""
}

func templateDir(t *testing.T) string { return filepath.Join(repoRoot(t), "templates") }

// TestPagesInheritGiteasChrome: pages wrap their content in base/head … base/footer,
// exactly as templates/user/dashboard/milestones.tmpl does, so chrome, themes, dark mode and
// Fomantic classes are inherited and the fork ships no stylesheet.
func TestPagesInheritGiteasChrome(t *testing.T) {
	for _, page := range forkPages {
		t.Run(page.template, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(templateDir(t), "delivery", page.template))
			require.NoError(t, err)
			body := string(raw)
			assert.True(t, strings.HasPrefix(body, `{{template "base/head" .}}`))
			assert.True(t, strings.HasSuffix(strings.TrimSpace(body), `{{template "base/footer" .}}`))
			assert.NotContains(t, body, "<link rel=\"stylesheet\"", "the fork ships no stylesheet")
			assert.NotContains(t, body, "<style", "the fork ships no stylesheet")
		})
	}
}

// allOperations pools both areas' published operations, since a page fetches whichever area
// it belongs to and this test checks any page against either.
func allOperations() []*hubapi.Operation {
	return append(append([]*hubapi.Operation{}, planningv1.Operations()...), deploymentsv1.Operations()...)
}

// forkPages is every page the fork serves, with the documented operation it is a client of.
// A page added without an entry here fails TestEveryPageIsListed, so no page can serve
// itself from anything but a published operation.
var forkPages = []struct {
	template string
	// endpoints are every operation path the page fetches. Each must be published, and
	// each must actually appear in the template, so neither list can rot.
	endpoints []string
	fetch     string
}{
	{
		template: "environment.tmpl",
		endpoints: []string{
			"/environments", "/environments/{id}", "/secret-scopes", "/secret-scopes/{id}",
			"/repos/{owner}/{repo}/environments/{name}/secrets",
		},
		fetch: "/environments?",
	},
	{
		template:  "grid.tmpl",
		endpoints: []string{"/grid", "/deployments", "/repos/{owner}/{repo}/releases"},
		fetch:     "/grid?",
	},
	{
		template:  "overview.tmpl",
		endpoints: []string{"/overview", "/overview/trends", "/overview/repos", "/runs"},
		fetch:     "/overview?",
	},
	{
		template:  "promote.tmpl",
		endpoints: []string{"/deployments"},
		fetch:     "/deployments",
	},
	{
		template:  "approvals.tmpl",
		endpoints: []string{"/approvals"},
		fetch:     "/approvals?",
	},
	{
		template:  "board.tmpl",
		endpoints: []string{"/board", "/board/cards/{issue_id}/column", "/board/cards/{issue_id}/lane"},
		fetch:     "/board?",
	},
	{
		template:  "timeline.tmpl",
		endpoints: []string{"/timeline"},
		fetch:     "/timeline?",
	},
}

// forkFragments are the templates a spoke embeds in an upstream page rather than the fork
// serving as a page of its own. They have no route and no API of their own to be a client of.
var forkFragments = map[string]bool{
	"navbar_entry.tmpl":         true,
	"release_environments.tmpl": true,
	"swimlanes.tmpl":            true,
}

func TestEveryPageIsListed(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(templateDir(t), "delivery"))
	require.NoError(t, err)

	pages := make([]string, 0, len(entries))
	for _, entry := range entries {
		if forkFragments[entry.Name()] {
			continue
		}
		pages = append(pages, entry.Name())
	}
	listed := make([]string, 0, len(forkPages))
	for _, p := range forkPages {
		listed = append(listed, p.template)
	}
	assert.ElementsMatch(t, pages, listed, "every page the fork ships is checked against its API")
}

// TestPageIsAClientOfItsAPI: the page fetches its data from a documented
// endpoint and the handler serves only the shell. A handler serving the UI alone is a defect.
func TestPageIsAClientOfItsAPI(t *testing.T) {
	for _, page := range forkPages {
		t.Run(page.template, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(templateDir(t), "delivery", page.template))
			require.NoError(t, err)
			body := string(raw)
			assert.Contains(t, body, page.fetch, "the page reads its rows over the API")
			assert.Contains(t, body, "suggested_action", "the page surfaces the API's suggested next action")

			published := map[string]bool{}
			for _, op := range allOperations() {
				published[op.Path] = true
			}
			for _, endpoint := range page.endpoints {
				assert.True(t, published[endpoint],
					"the page's endpoint %s must be a published operation", endpoint)
				// The template has to name it, so an endpoint listed here but no longer
				// fetched is caught rather than standing as a claim about a dead page. A page
				// may name the documented path verbatim, as board.tmpl does, or interpolate the
				// repository into it, as grid.tmpl does; either one names the operation.
				interpolated := strings.ReplaceAll(strings.ReplaceAll(endpoint, "{owner}", "${row.repo_full_name}"), "/{repo}", "")
				assert.True(t, strings.Contains(body, endpoint) || strings.Contains(body, interpolated),
					"the page fetches %s", endpoint)
			}
		})
	}
}

// gateFor is which settings gate TestRoutesAreRegisteredBehindTheGate expects on each
// pattern: routers/web/deployments' routes carry DeploymentsPagesEnabled, and
// routers/web/planning's carry PlanningPagesEnabled, so switching one area off never
// touches the other's pages.
var gateFor = map[string]func(*context.Context){
	"/delivery/environments":                  hub_web.DeploymentsPagesEnabled,
	"/delivery/environments/{name}":           hub_web.DeploymentsPagesEnabled,
	"/delivery/environments/{id}/edit":        hub_web.DeploymentsPagesEnabled,
	"/delivery/grid":                          hub_web.DeploymentsPagesEnabled,
	"/delivery/ci":                            hub_web.DeploymentsPagesEnabled,
	"/delivery/promote":                       hub_web.DeploymentsPagesEnabled,
	"/delivery/approvals":                     hub_web.DeploymentsPagesEnabled,
	"/delivery/environments/{name}/approvals": hub_web.DeploymentsPagesEnabled,
	"/delivery/board":                         hub_web.PlanningPagesEnabled,
	"/delivery/timeline":                      hub_web.PlanningPagesEnabled,
}

func TestRoutesAreRegisteredBehindTheGate(t *testing.T) {
	r := &recordingRouter{}
	RegisterRoutes(r, "signin")
	assert.Equal(t, []string{
		"/delivery/environments", "/delivery/environments/{name}",
		"/delivery/environments/{id}/edit", "/delivery/grid",
		"/delivery/ci",
		"/delivery/promote",
		"/delivery/approvals", "/delivery/environments/{name}/approvals",
		"/delivery/board", "/delivery/timeline",
	}, r.patterns)
	for i, pattern := range r.patterns {
		handlers := r.handlers[i]
		require.Len(t, handlers, 3, "each page sits behind reqSignIn and the settings gate")
		wantGate, ok := gateFor[pattern]
		require.True(t, ok, "%s has no expected gate declared in this test", pattern)
		assert.True(t, sameFunc(handlers[1], wantGate),
			"%s must sit behind its own area's settings gate, not the other area's", pattern)
	}
}

// sameFunc compares two middleware values by the function they point at: func values are
// otherwise comparable only to nil, and the router type-erases them to any.
func sameFunc(a, b any) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

type recordingRouter struct {
	patterns []string
	handlers [][]any
}

func (r *recordingRouter) Get(pattern string, h ...any) {
	r.patterns = append(r.patterns, pattern)
	r.handlers = append(r.handlers, h)
}
