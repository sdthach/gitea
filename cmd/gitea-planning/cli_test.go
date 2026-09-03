// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"gitea.dev/cmd/hubcli"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	previous := hubcli.Transport
	hubcli.Transport = rec
	t.Cleanup(func() { hubcli.Transport = previous })
	t.Setenv("GITEA_PLANNING_SERVER", "https://gitea.example.invalid")
	t.Setenv("GITEA_PLANNING_TOKEN", "t0ken")
	t.Setenv("FORGE_TOKEN", "")
	t.Setenv("GITEA_TOKEN", "")
}

func exec(t *testing.T, rec *recorder, args ...string) (string, string, *hubcli.Error) {
	t.Helper()
	var stdout, stderr strings.Builder
	err := hubcli.Run(config, args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// TestEveryCommandComposesItsRequest is one test per command.
func TestEveryCommandComposesItsRequest(t *testing.T) {
	cases := map[string]struct {
		args       []string
		wantPath   string
		wantMethod string
	}{
		"board": {
			[]string{"board", "--filter", "repo_id=1", "--filter", "project_id=5", "--filter", "group_by=type"},
			"/api/planning/v1/board", "",
		},
		"roadmap": {[]string{"roadmap", "--filter", "repo_id=1"}, "/api/planning/v1/roadmap", ""},
		"board-move-column": {
			[]string{"board-move-column", "--repo", "acme/web", "--project-id", "5", "--column-id", "12", "9042"},
			"/api/planning/v1/board/cards/9042/column", http.MethodPost,
		},
		"board-move-group": {
			[]string{"board-move-group", "--repo", "acme/web", "--project-id", "5", "--group-by", "type", "--group", "bug", "9042"},
			"/api/planning/v1/board/cards/9042/group", http.MethodPost,
		},
		"issue-move-milestone": {
			[]string{"issue-move-milestone", "--repo", "acme/widgets", "--milestone-id", "2", "9042"},
			"/api/planning/v1/issues/9042/milestone", http.MethodPost,
		},
		"issue-move-group": {
			[]string{"issue-move-group", "--repo", "acme/widgets", "--group-by", "type", "--group", "bug", "9042"},
			"/api/planning/v1/issues/9042/group", http.MethodPost,
		},
		"issue-set-dates": {
			[]string{"issue-set-dates", "--repo", "acme/widgets", "--start", "2026-03-01", "9042"},
			"/api/planning/v1/issues/9042/dates", http.MethodPost,
		},
		"milestone-create": {
			[]string{"milestone-create", "--repo", "acme/widgets", "--title", "Sprint 9"},
			"/api/planning/v1/milestones", http.MethodPost,
		},
		"issue-create": {
			[]string{"issue-create", "--repo", "acme/widgets", "--title", "Wire it"},
			"/api/planning/v1/issues", http.MethodPost,
		},
		"issue": {[]string{"issue", "9042"}, "/api/planning/v1/issues/9042", ""},
		"issue-set-parent": {
			[]string{"issue-set-parent", "--repo", "acme/widgets", "--parent-issue-id", "7", "9042"},
			"/api/planning/v1/issues/9042/parent", http.MethodPut,
		},
		"issue-clear-parent": {
			[]string{"issue-clear-parent", "--repo", "acme/widgets", "9042"},
			"/api/planning/v1/issues/9042/parent", http.MethodDelete,
		},
		"issue-set-start": {
			[]string{"issue-set-start", "--repo", "acme/widgets", "--start", "2026-03-01", "9042"},
			"/api/planning/v1/issues/9042/schedule", http.MethodPut,
		},
		"issue-clear-start": {
			[]string{"issue-clear-start", "--repo", "acme/widgets", "9042"},
			"/api/planning/v1/issues/9042/schedule", http.MethodDelete,
		},
		"milestone-set-start": {
			[]string{"milestone-set-start", "--repo", "acme/widgets", "--start", "2026-03-01", "7"},
			"/api/planning/v1/milestones/7/schedule", http.MethodPut,
		},
		"milestone-clear-start": {
			[]string{"milestone-clear-start", "--repo", "acme/widgets", "7"},
			"/api/planning/v1/milestones/7/schedule", http.MethodDelete,
		},
		"issue-set-estimate": {
			[]string{"issue-set-estimate", "--repo", "acme/widgets", "--time-estimate", "3h", "9042"},
			"/api/planning/v1/issues/9042/estimate", http.MethodPut,
		},
		"issue-types": {
			[]string{"issue-types", "--filter", "repo_id=1"},
			"/api/planning/v1/issue-types", "",
		},
		"issue-type-create": {
			[]string{"issue-type-create", "--repo-id", "1", "--name", "bug", "--color", "#d1242f", "--icon", "octicon-bug", "--rank", "3"},
			"/api/planning/v1/issue-types", http.MethodPost,
		},
		"issue-type-update": {
			[]string{"issue-type-update", "--name", "bug", "--color", "#d1242f", "--icon", "octicon-bug", "--rank", "3", "7"},
			"/api/planning/v1/issue-types/7", http.MethodPut,
		},
		"issue-type-delete": {
			[]string{"issue-type-delete", "--force", "7"},
			"/api/planning/v1/issue-types/7", http.MethodDelete,
		},
		"issue-set-type": {
			[]string{"issue-set-type", "--repo", "acme/widgets", "--type-id", "7", "9042"},
			"/api/planning/v1/issues/9042/type", http.MethodPut,
		},
		"issue-clear-type": {
			[]string{"issue-clear-type", "--repo", "acme/widgets", "9042"},
			"/api/planning/v1/issues/9042/type", http.MethodDelete,
		},
		"issue-type-assignments": {
			[]string{"issue-type-assignments", "--filter", "repo_id=1"},
			"/api/planning/v1/issue-type-assignments", "",
		},
		"projects": {
			[]string{"projects", "--filter", "repo_id=1"},
			"/api/planning/v1/projects", "",
		},
		"project-views": {
			[]string{"project-views", "--filter", "repo=user2/repo1", "5"},
			"/api/planning/v1/projects/5/views", "",
		},
		"project-view-save": {
			[]string{"project-view-save", "--repo", "acme/widgets", "--name", "open bugs", "--query", "state:open", "5"},
			"/api/planning/v1/projects/5/views", http.MethodPost,
		},
		"project-view-delete": {
			[]string{"project-view-delete", "--repo", "acme/widgets", "5", "9"},
			"/api/planning/v1/projects/5/views/9", http.MethodDelete,
		},
	}
	require.Len(t, cases, len(Commands), "every command needs a test; add one when an endpoint is added")

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var isList bool
			for _, cmd := range Commands {
				if cmd.Name == name {
					isList = cmd.IsList
				}
			}
			body := "[]"
			if !isList {
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

// TestProjectViewsCommandSendsRepoOnTheQueryString: getProjectViews takes repo as a
// non-grammar query parameter (its op has no filter grammar of its own), so the generated
// client must still carry it onto the request's query string.
func TestProjectViewsCommandSendsRepoOnTheQueryString(t *testing.T) {
	rec := &recorder{body: "{}"}
	withRecorder(t, rec)

	_, _, err := exec(t, rec, "project-views", "--filter", "repo=user2/repo1", "5")
	require.Nil(t, err)
	require.Len(t, rec.requests, 1)
	assert.Equal(t, "user2/repo1", rec.requests[0].URL.Query().Get("repo"))

	found := false
	for _, cmd := range Commands {
		if cmd.Name == "project-views" {
			found = true
			assert.Contains(t, cmd.QueryParams, "repo", "the generated command must document repo as a query parameter")
		}
	}
	require.True(t, found)
}
