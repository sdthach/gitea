// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	htmltemplate "html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

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

// templateStubs stands in for the helpers Gitea injects at render time, so a fork template
// can be parsed and executed outside a running server. ctx carries the CSP nonce every
// inline script names.
func templateStubs() map[string]any {
	return map[string]any{
		"AppSubUrl": func() string { return "" },
		"ctx":       func() any { return struct{ CspScriptNonce string }{CspScriptNonce: "test-nonce"} },
	}
}

// TestForkTemplatesParse catches a template that would only fail when a page is served.
func TestForkTemplatesParse(t *testing.T) {
	dir := filepath.Join(templateDir(t), "delivery")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	for _, entry := range entries {
		t.Run(entry.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			require.NoError(t, err)
			_, err = template.New(entry.Name()).Funcs(template.FuncMap(templateStubs())).Parse(string(raw))
			require.NoError(t, err)
		})
	}
}

// TestEveryInlineScriptCarriesTheCspNonce is what keeps the pages alive in a deployment
// that sends Gitea's Content Security Policy: the policy admits an inline script only with
// the request's nonce, so a script without one is dropped and the page renders a shell that
// never loads. Nothing server-rendered says so, which is why this is checked in source.
func TestEveryInlineScriptCarriesTheCspNonce(t *testing.T) {
	dir := filepath.Join(templateDir(t), "delivery")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	scripts := 0
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		for line := range strings.SplitSeq(string(raw), "\n") {
			if !strings.Contains(line, "<script") {
				continue
			}
			scripts++
			assert.Contains(t, line, `nonce="{{ctx.CspScriptNonce}}"`,
				"%s opens a script the policy would drop", entry.Name())
		}
	}
	assert.Greater(t, scripts, 5, "the scan must actually have found the fork's scripts")
}

// TestNavbarSpokeDelegatesToAHubTemplate: the single upstream template edit is one
// line delegating into a template the fork owns.
func TestNavbarSpokeDelegatesToAHubTemplate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateDir(t), "base", "head_navbar.tmpl"))
	require.NoError(t, err)

	var spokeLines []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.Contains(line, `"delivery/`) {
			spokeLines = append(spokeLines, strings.TrimSpace(line))
		}
	}
	require.Len(t, spokeLines, 1, "the navbar carries exactly one fork line")
	assert.Contains(t, spokeLines[0], `{{template "delivery/navbar_entry" .}}`)

	_, err = os.Stat(filepath.Join(templateDir(t), "delivery", "navbar_entry.tmpl"))
	require.NoError(t, err, "the navbar spoke names a template the fork ships")
}

// TestEnvironmentPageEscapesItsData executes the page through html/template, the renderer
// Gitea uses, so a value interpolated into the page's inline script is proven to arrive as a
// quoted JavaScript string rather than as raw source. Parsing alone would not catch this:
// contextual escaping happens at execution.
func TestEnvironmentPageEscapesItsData(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateDir(t), "delivery", "environment.tmpl"))
	require.NoError(t, err)

	tmpl := htmltemplate.New("root").Funcs(htmltemplate.FuncMap(templateStubs()))
	_, err = tmpl.New("base/head").Parse("<html><body>")
	require.NoError(t, err)
	_, err = tmpl.New("base/footer").Parse("</body></html>")
	require.NoError(t, err)
	_, err = tmpl.New("environment").Parse(string(raw))
	require.NoError(t, err)

	var out strings.Builder
	require.NoError(t, tmpl.ExecuteTemplate(&out, "environment", map[string]any{
		"Title":           "Environment: prod",
		"EnvironmentName": `prod";alert(1);//`,
		"DeliveryAPIBase": "/api/deployments/v1",
	}))
	body := out.String()
	assert.Contains(t, body, `const base = "/api/deployments/v1";`)
	assert.NotContains(t, body, `const wanted = "prod";alert(1);//";`, "an environment name must not be able to close the string literal")
	assert.Contains(t, body, "alert", "the value is still rendered, just escaped")
}
