// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"

	"gitea.dev/models/db"
	delivery "gitea.dev/models/deployments"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/services/context"
	"gitea.dev/services/hub/query"

	"xorm.io/builder"
)

// releaseSpec is the releases resource's whitelist declaration. Releases are finite and
// stable, so they page by 1-based page with X-Total-Count and a Link header, Gitea's own
// convention.
var releaseSpec = query.Spec{
	Resource: "releases",
	Fields: []query.Field{
		{Name: "id", Column: "id", Kind: query.KindInt},
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt},
		{Name: "tag_name", Column: "tag_name", Kind: query.KindString},
		{Name: "is_prerelease", Column: "is_prerelease", Kind: query.KindBool},
		{Name: "created_unix", Column: "created_unix", Kind: query.KindTime},
	},
	SortFields:   []string{"id", "tag_name", "created_unix"},
	DefaultSort:  "created_unix",
	DefaultOrder: query.OrderDesc,
	PrimaryKey:   "id",
	SearchFields: []string{"tag_name", "title"},
	Expands:      []string{"deployments"},
	Paging:       query.PagingOffset,
}

// Release is the delivery view of a release. Releases own version identity: a deployment
// points at a release tag and carries no version string to be parsed.
//
// Nothing here is synced, cached or mirrored — it is read from Gitea's own Release model at
// render time, so a release cut outside this feature appears immediately.
type Release struct {
	ID      int64  `json:"id"`
	RepoID  int64  `json:"repo_id"`
	TagName string `json:"tag_name"`
	Title   string `json:"title"`
	// Target is the release's own commitish. The deploy job posts its commit status
	// against this SHA; posting against any other passes every API check while leaving the
	// native release page blank.
	Target       string                 `json:"target"`
	SHA          string                 `json:"sha"`
	URL          string                 `json:"url"`
	IsPrerelease bool                   `json:"is_prerelease"`
	CreatedUnix  int64                  `json:"created_unix"`
	Artifacts    []*Artifact            `json:"artifacts,omitempty"`
	Deployments  []*delivery.Deployment `json:"deployments,omitempty"`
}

// Artifact is one of the release's attachments. Artifacts come from the release, not from a
// second store the fork would have to keep in step.
type Artifact struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

func convertRelease(ctx *context.APIContext, r *repo_model.Release) *Release {
	out := &Release{
		ID: r.ID, RepoID: r.RepoID, TagName: r.TagName, Title: r.Title,
		Target: r.Target, SHA: r.Sha1, IsPrerelease: r.IsPrerelease,
		CreatedUnix: int64(r.CreatedUnix),
	}
	if r.Repo != nil {
		out.URL = r.HTMLURL()
	}
	for _, a := range r.Attachments {
		out.Artifacts = append(out.Artifacts, &Artifact{Name: a.Name, Size: a.Size, URL: a.DownloadURL(ctx)})
	}
	return out
}

func listReleasesEndpoint() *endpoint {
	return &endpoint{
		Op: &Operation{
			ID: "listReleases", Method: http.MethodGet, Path: "/repos/{owner}/{repo}/releases",
			Summary: "List a repository's releases, the rows of the grid",
			Description: "Read from Gitea's own Release model at render time. Nothing is synced, cached or mirrored, " +
				"so a release cut outside this feature appears immediately. " +
				"Expand deployments to get every deployment of each release.",
			Tag: "releases", PathParams: ownerRepoParams,
			Query: &releaseSpec, Response: "Release", ResponseIs: "array",
		},
		Handler: ListReleases,
	}
}

// ListReleases answers GET /repos/{owner}/{repo}/releases.
func ListReleases(ctx *context.APIContext) {
	repo, ok := repoWithActions(ctx, false)
	if !ok {
		return
	}
	q, ok := parseQuery(ctx, releaseSpec)
	if !ok {
		return
	}

	scope := builder.Eq{"repo_id": repo.ID, "is_draft": false, "is_tag": false}
	releases := make([]*repo_model.Release, 0, q.Limit)
	total, err := db.GetEngine(ctx).Where(q.Cond().And(scope)).
		OrderBy(q.OrderBy()).Limit(q.Limit, q.Offset()).FindAndCount(&releases)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if err := repo_model.GetReleaseAttachments(ctx, releases...); err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	out := make([]*Release, 0, len(releases))
	for _, r := range releases {
		r.Repo = repo
		out = append(out, convertRelease(ctx, r))
	}
	if err := expandReleases(ctx, q.Expand, repo.ID, out); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	renderPage(ctx, q, total, out)
}

// expandReleases fills the whitelisted sub-resources, one level deep.
func expandReleases(ctx *context.APIContext, expand []string, repoID int64, rows []*Release) error {
	for _, name := range expand {
		if name != "deployments" {
			continue
		}
		for _, row := range rows {
			cond := builder.Eq{"repo_id": repoID, "release_tag": row.TagName}
			deployments, err := delivery.FindDeployments(ctx, cond, "created_unix ASC, id ASC", query.MaxLimit)
			if err != nil {
				return err
			}
			row.Deployments = deployments
		}
	}
	return nil
}
