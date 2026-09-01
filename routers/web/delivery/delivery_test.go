// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	htmltemplate "html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	deliveryv1 "gitea.dev/routers/api/delivery/v1"

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
			_, err = template.New(entry.Name()).Funcs(template.FuncMap{
				"AppSubUrl": func() string { return "" },
			}).Parse(string(raw))
			require.NoError(t, err)
		})
	}
}

// TestPagesInheritGiteasChrome is F7: pages wrap their content in base/head … base/footer,
// exactly as templates/user/dashboard/milestones.tmpl does, so chrome, themes, dark mode and
// Fomantic classes are inherited and the fork ships no stylesheet.
func TestPagesInheritGiteasChrome(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateDir(t), "delivery", "environment.tmpl"))
	require.NoError(t, err)
	body := string(raw)
	assert.True(t, strings.HasPrefix(body, `{{template "base/head" .}}`))
	assert.True(t, strings.HasSuffix(strings.TrimSpace(body), `{{template "base/footer" .}}`))
	assert.NotContains(t, body, "<link rel=\"stylesheet\"", "the fork ships no stylesheet")
	assert.NotContains(t, body, "<style", "the fork ships no stylesheet")
}

// TestPageIsAClientOfItsAPI is E18/I14: the page fetches its data from a documented
// endpoint and the handler serves only the shell. A handler serving the UI alone is a defect.
func TestPageIsAClientOfItsAPI(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateDir(t), "delivery", "environment.tmpl"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "/environments?", "the page reads its rows over the API")
	assert.Contains(t, string(raw), "suggested_action", "the page surfaces the API's suggested next action (A21)")

	documented := false
	for _, op := range deliveryv1.Operations() {
		if op.Path == "/environments" {
			documented = true
		}
	}
	assert.True(t, documented, "the page's endpoint must be a published operation")
}

// TestNavbarSpokeDelegatesToAHubTemplate is F2: the single upstream template edit is one
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

func TestRoutesAreRegisteredBehindTheGate(t *testing.T) {
	r := &recordingRouter{}
	RegisterRoutes(r, "signin")
	assert.Equal(t, []string{"/delivery/environments", "/delivery/environments/{name}"}, r.patterns)
	for _, handlers := range r.handlers {
		require.Len(t, handlers, 3, "each page sits behind reqSignIn and the settings gate (F13)")
	}
}

type recordingRouter struct {
	patterns []string
	handlers [][]any
}

func (r *recordingRouter) Get(pattern string, h ...any) {
	r.patterns = append(r.patterns, pattern)
	r.handlers = append(r.handlers, h)
}

// TestEnvironmentPageEscapesItsData executes the page through html/template, the renderer
// Gitea uses, so a value interpolated into the page's inline script is proven to arrive as a
// quoted JavaScript string rather than as raw source. Parsing alone would not catch this:
// contextual escaping happens at execution.
func TestEnvironmentPageEscapesItsData(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateDir(t), "delivery", "environment.tmpl"))
	require.NoError(t, err)

	tmpl := htmltemplate.New("root").Funcs(htmltemplate.FuncMap{
		"AppSubUrl": func() string { return "" },
	})
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
		"DeliveryAPIBase": "/api/delivery/v1",
	}))
	body := out.String()
	assert.Contains(t, body, `const base = "/api/delivery/v1";`)
	assert.NotContains(t, body, `const wanted = "prod";alert(1);//";`, "an environment name must not be able to close the string literal")
	assert.Contains(t, body, "alert", "the value is still rendered, just escaped")
}
