// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hubroutes

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	deploymentsv1 "gitea.dev/routers/api/deployments/v1"
	hubapi "gitea.dev/routers/api/hub"
	planningv1 "gitea.dev/routers/api/planning/v1"
	"gitea.dev/services/context"
	"gitea.dev/services/contexttest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forkPage describes a page the fork serves, with the documented operations it is a client of.
type forkPage struct {
	dir      string
	template string
	// endpoints are every operation path the page fetches. Each must be published, and
	// each must actually appear in the template (or client, when set), so neither list can rot.
	endpoints []string
	fetch     string
	// client is the bundled TypeScript module that is a client of the API instead of the
	// template itself, for a page whose figures are fetched by mounted Vue rather than an
	// inline script. Path relative to the repository root.
	client string
}

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

// forkPages combines deployments and planning pages in order.
func forkPages() []forkPage {
	return append(append([]forkPage{}, deploymentsPages...), planningPages...)
}

// forkFragments combines deployments and planning fragments.
func forkFragments() map[string]map[string]bool {
	return map[string]map[string]bool{
		"deployments": deploymentsFragments,
		"planning":    planningFragments,
	}
}

// gateFor combines deployments and planning gates.
func gateFor() map[string]func(*context.Context) {
	combined := make(map[string]func(*context.Context))
	maps.Copy(combined, deploymentsGates)
	maps.Copy(combined, planningGates)
	return combined
}

// expectedPatterns combines deployments and planning patterns in order.
func expectedPatterns() []string {
	return append(append([]string{}, deploymentsPatterns...), planningPatterns...)
}

// TestPagesInheritGiteasChrome: pages wrap their content in base/head … base/footer,
// exactly as templates/user/dashboard/milestones.tmpl does, so chrome, themes, dark mode and
// Fomantic classes are inherited and the fork ships no stylesheet.
func TestPagesInheritGiteasChrome(t *testing.T) {
	for _, page := range forkPages() {
		t.Run(page.template, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(templateDir(t), page.dir, page.template))
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

func TestEveryPageIsListed(t *testing.T) {
	var pages []struct{ dir, name string }
	for dir, fragments := range forkFragments() {
		entries, err := os.ReadDir(filepath.Join(templateDir(t), dir))
		require.NoError(t, err)
		for _, entry := range entries {
			if fragments[entry.Name()] {
				continue
			}
			pages = append(pages, struct{ dir, name string }{dir, entry.Name()})
		}
	}
	listed := make([]struct{ dir, name string }, 0, len(forkPages()))
	for _, p := range forkPages() {
		listed = append(listed, struct{ dir, name string }{p.dir, p.template})
	}
	assert.ElementsMatch(t, pages, listed, "every page the fork ships is checked against its API")
}

// TestPageIsAClientOfItsAPI: the page fetches its data from a documented
// endpoint and the handler serves only the shell. A handler serving the UI alone is a defect.
func TestPageIsAClientOfItsAPI(t *testing.T) {
	for _, page := range forkPages() {
		t.Run(page.template, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(templateDir(t), page.dir, page.template))
			require.NoError(t, err)
			body := string(raw)

			checked := body
			if page.client != "" {
				clientRaw, err := os.ReadFile(filepath.Join(repoRoot(t), page.client))
				require.NoError(t, err)
				checked = string(clientRaw)
				assert.Regexp(t, `data-global-init="init[A-Za-z]+"`, body,
					"the page mounts the bundled feature that is the API's actual client")
			}
			assert.Contains(t, checked, page.fetch, "the page reads its rows over the API")
			assert.Contains(t, checked, "suggested_action", "the page surfaces the API's suggested next action")

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
				// repository into it, as matrix.tmpl does; either one names the operation.
				if page.client != "" {
					// A client's own path table spells every endpoint as a complete quoted
					// literal (docs/planning/openapi.json's own path, verbatim), so requiring
					// the closing quote catches a renamed literal that a plain substring check
					// would miss against a sibling endpoint sharing the same prefix, such as
					// /issues/{issue_id}/estimate for /issues/{issue_id}.
					assert.True(t, quotedLiteral(checked, endpoint),
						"the client %s must name the exact endpoint %s as a quoted literal, not merely contain it as a sibling path's prefix", page.client, endpoint)
					continue
				}
				interpolated := strings.ReplaceAll(strings.ReplaceAll(endpoint, "{owner}", "${row.repo_full_name}"), "/{repo}", "")
				assert.True(t, strings.Contains(checked, endpoint) || strings.Contains(checked, interpolated),
					"the page fetches %s", endpoint)
			}
		})
	}
}

// quotedLiteral reports whether endpoint appears in body as a complete quoted string literal,
// bounded by a matching quote character on both sides, so a sibling path that merely starts
// with endpoint (a longer literal sharing its prefix) does not satisfy the check.
func quotedLiteral(body, endpoint string) bool {
	for _, quote := range []string{"'", `"`, "`"} {
		if strings.Contains(body, quote+endpoint+quote) {
			return true
		}
	}
	return false
}

// redirectPatterns is every pattern registerRedirects mounts. Each is a plain 303 to its
// replacement: the new page underneath enforces its own sign-in and settings gate, so a
// redirect needs neither.
var redirectPatterns = []string{
	"/delivery/environments", "/delivery/environments/{name}",
	"/delivery/environments/{id}/edit", "/delivery/environments/{name}/approvals",
	"/delivery/grid", "/delivery/promote", "/delivery/approvals", "/delivery/ci",
	"/delivery/board", "/delivery/timeline",
}

func TestRoutesAreRegisteredBehindTheGate(t *testing.T) {
	r := &recordingRouter{}
	RegisterRoutes(r, "signin")

	pagePatterns := make([]string, 0, len(gateFor()))
	for _, pattern := range r.patterns[:len(r.patterns)-len(redirectPatterns)] {
		pagePatterns = append(pagePatterns, pattern)
	}
	assert.Equal(t, expectedPatterns(), pagePatterns, "every pattern is registered in order")
	assert.Equal(t, redirectPatterns, r.patterns[len(r.patterns)-len(redirectPatterns):],
		"the old /delivery/* URLs are mounted last, so they never shadow a current page")

	gates := gateFor()
	for i, pattern := range pagePatterns {
		handlers := r.handlers[i]
		require.Len(t, handlers, 3, "each page sits behind reqSignIn and the settings gate")
		wantGate, ok := gates[pattern]
		require.True(t, ok, "%s has no expected gate declared in this test", pattern)
		assert.True(t, sameFunc(handlers[1], wantGate),
			"%s must sit behind its own area's settings gate, not the other area's", pattern)
	}
	for i := range redirectPatterns {
		handlers := r.handlers[len(pagePatterns)+i]
		require.Len(t, handlers, 1, "a redirect needs no sign-in check or gate of its own")
	}
}

// TestOldURLsRedirect exercises every handler registerRedirects mounts end to end: a 303 to
// its replacement, with a path parameter or a query string carried over unchanged.
func TestOldURLsRedirect(t *testing.T) {
	r := &recordingRouter{}
	RegisterRoutes(r, "signin")

	cases := []struct {
		pattern, path, query, wantLocation string
		pathParams                         map[string]string
	}{
		{pattern: "/delivery/environments", wantLocation: "/deployments/environments"},
		{
			pattern: "/delivery/environments/{name}", wantLocation: "/deployments/environments/prod",
			pathParams: map[string]string{"name": "prod"},
		},
		{
			pattern: "/delivery/environments/{id}/edit", wantLocation: "/deployments/environments/1/edit",
			pathParams: map[string]string{"id": "1"},
		},
		{
			pattern: "/delivery/environments/{name}/approvals", wantLocation: "/deployments/environments/prod/reviews",
			pathParams: map[string]string{"name": "prod"},
		},
		{pattern: "/delivery/grid", wantLocation: "/deployments"},
		{
			pattern: "/delivery/promote", wantLocation: "/deployments/new?repo=user2%2Frepo1&environment=prod&release_tag=v1",
			query: "repo=user2%2Frepo1&environment=prod&release_tag=v1",
		},
		{pattern: "/delivery/approvals", wantLocation: "/deployments/reviews"},
		{pattern: "/delivery/ci", wantLocation: "/deployments/insights"},
		{pattern: "/delivery/board", wantLocation: "/planning/board"},
		{pattern: "/delivery/timeline", wantLocation: "/planning/roadmap"},
	}
	for _, c := range cases {
		t.Run(c.pattern, func(t *testing.T) {
			handler := redirectHandlerFor(t, r, c.pattern)
			reqPath := c.pattern
			if c.query != "" {
				reqPath += "?" + c.query
			}
			ctx, resp := contexttest.MockContext(t, reqPath)
			for name, value := range c.pathParams {
				ctx.SetPathParam(name, value)
			}
			handler(ctx)
			assert.Equal(t, 303, resp.Code)
			assert.Equal(t, c.wantLocation, resp.Header().Get("Location"))
		})
	}
}

// redirectHandlerFor returns the single handler registerRedirects mounted for pattern.
func redirectHandlerFor(t *testing.T, r *recordingRouter, pattern string) func(*context.Context) {
	t.Helper()
	for i, p := range r.patterns {
		if p != pattern {
			continue
		}
		require.Len(t, r.handlers[i], 1, "a redirect needs no sign-in check or gate of its own")
		handler, ok := r.handlers[i][0].(func(*context.Context))
		require.True(t, ok, "%s must register a plain *context.Context handler", pattern)
		return handler
	}
	t.Fatalf("%s was never registered", pattern)
	return nil
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

func (r *recordingRouter) Post(pattern string, h ...any) {
	r.patterns = append(r.patterns, pattern)
	r.handlers = append(r.handlers, h)
}
