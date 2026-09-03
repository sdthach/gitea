// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"io"
	"net/http"
	"strings"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	"gitea.dev/models/perm/access"
	planning_model "gitea.dev/models/planning"
	project_model "gitea.dev/models/project"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/json"
	"gitea.dev/modules/optional"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	"gitea.dev/services/hub/query"
)

// maxProjectViewBody caps a saved view's body: a name, a query string and a repo — never an
// upload.
const maxProjectViewBody = 8 << 10

// maxViewNameLen and maxViewQueryLen are the saved view's own field limits.
const (
	maxViewNameLen  = 100
	maxViewQueryLen = 4 << 10
)

// LabelRef is the id/name/color a label carries onto a board or roadmap projection — enough
// for a picker to offer it and a card to render it, nothing a label edit would need.
type LabelRef struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// repoLabels lists a repository's own labels plus its owning organization's, the set a
// card's label picker offers. Labels scoped to a DIFFERENT organization or repository never
// appear here: Gitea's own label scoping is not re-implemented, only read.
func repoLabels(ctx *context.APIContext, repo *repo_model.Repository) ([]LabelRef, error) {
	rows, err := issues_model.GetLabelsByRepoID(ctx, repo.ID, "", db.ListOptionsAll)
	if err != nil {
		return nil, err
	}
	out := make([]LabelRef, 0, len(rows))
	for _, l := range rows {
		out = append(out, LabelRef{ID: l.ID, Name: l.Name, Color: l.Color})
	}
	if err := repo.LoadOwner(ctx); err != nil {
		return nil, err
	}
	if repo.Owner.IsOrganization() {
		orgRows, err := issues_model.GetLabelsByOrgID(ctx, repo.OwnerID, "", db.ListOptionsAll)
		if err != nil {
			return nil, err
		}
		for _, l := range orgRows {
			out = append(out, LabelRef{ID: l.ID, Name: l.Name, Color: l.Color})
		}
	}
	return out, nil
}

// repoProjectsEnabled is boardAvailable's own condition, as a value rather than a rendered
// refusal: whether repository-level boards are usable on repo at all, instance-wide setting
// included. The picker publishes it per repository so a client can grey out a repo with no
// board rather than let its own board fetch fail.
func repoProjectsEnabled(ctx *context.APIContext, repo *repo_model.Repository) bool {
	if unit.TypeProjects.UnitGlobalDisabled() {
		return false
	}
	projectsUnit, err := repo.GetUnit(ctx, unit.TypeProjects)
	return err == nil && projectsUnit.ProjectsConfig().IsProjectsAllowed(repo_model.ProjectsModeRepo)
}

// ProjectsPickerRepo is one repository as the /projects picker offers it: enough to name it
// and to know whether it has a board worth opening.
type ProjectsPickerRepo struct {
	ID              int64  `json:"id"`
	FullName        string `json:"full_name"`
	Owner           string `json:"owner"`
	Name            string `json:"name"`
	Private         bool   `json:"private"`
	ProjectsEnabled bool   `json:"projects_enabled"`
}

// ProjectsPickerProject is one Gitea project as the /projects picker offers it.
type ProjectsPickerProject struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	RepoID   int64  `json:"repo_id"`
	OwnerID  int64  `json:"owner_id"`
	Type     int    `json:"type"`
	IsClosed bool   `json:"is_closed"`
	Columns  int64  `json:"columns"`
}

// ProjectsPage is the /projects picker's response: without repo_id, every repository the
// caller can read the Issues unit of and no project; with repo_id, that one repository and
// the boards filed under it.
type ProjectsPage struct {
	Repos    []ProjectsPickerRepo    `json:"repos"`
	Projects []ProjectsPickerProject `json:"projects"`
}

// projectsSpec is the /projects picker's whitelist declaration.
var projectsSpec = query.Spec{
	Resource: "projects",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "is_closed", Column: "is_closed", Kind: query.KindBool, Ops: []query.Op{query.OpEq}},
	},
	PrimaryKey: "id",
	Paging:     query.PagingOffset,
}

func listProjectsEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "listProjects", Method: http.MethodGet, Path: "/projects",
			Summary: "The repositories and boards a planning page can open",
			Description: "Without repo_id every repository the caller can read the Issues unit of, and no project: " +
				"a picker lists repositories before a board is chosen. With repo_id, that one repository — 404 when it " +
				"is not readable, never naming it — and the boards filed under it: its own repository projects, plus, " +
				"when its owner is an organization, that organization's own projects. A repository whose Projects unit " +
				"is unusable, instance-wide or on the repository itself, answers projects_unavailable exactly as " +
				BasePath + "/board does.",
			Tag: "projects", Query: &projectsSpec, Response: "ProjectsPage", ResponseIs: "object",
			CLINames: []string{"projects"},
		},
		Handler: ListProjects,
	}
}

// ListProjects answers GET /projects.
func ListProjects(ctx *context.APIContext) {
	q, ok := hubapi.ParseQuery(ctx, projectsSpec)
	if !ok {
		return
	}
	repoID := hubapi.EqualityFilterInt(q, "repo_id")
	if repoID <= 0 {
		listProjectPickerRepos(ctx, q)
		return
	}

	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository with that id is visible to you",
			"Check the id against "+BasePath+"/projects.")
		return
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if !boardReadable(ctx, perm) {
		return
	}
	if !boardAvailable(ctx, repo) {
		return
	}

	isClosed := equalityFilterBool(q, "is_closed")
	projects, err := db.Find[project_model.Project](ctx, project_model.SearchOptions{
		RepoID: repo.ID, Type: project_model.TypeRepository, IsClosed: isClosed,
		ListOptions: db.ListOptionsAll,
	})
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if err := repo.LoadOwner(ctx); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if repo.Owner.IsOrganization() {
		orgProjects, err := db.Find[project_model.Project](ctx, project_model.SearchOptions{
			OwnerID: repo.OwnerID, Type: project_model.TypeOrganization, IsClosed: isClosed,
			ListOptions: db.ListOptionsAll,
		})
		if err != nil {
			ctx.APIErrorInternal(err)
			return
		}
		projects = append(projects, orgProjects...)
	}

	// Both queries above fetch every matching row (repo projects then org projects, each in
	// stable id order) so the true total is known before paging the merge in memory — a
	// hub list response's total and Link headers describe the whole result, not one query.
	total := int64(len(projects))
	start := min(q.Offset(), len(projects))
	end := min(start+q.Limit, len(projects))
	projects = projects[start:end]

	out := &ProjectsPage{
		Repos: []ProjectsPickerRepo{{
			ID: repo.ID, FullName: repo.FullName(), Owner: repo.OwnerName, Name: repo.Name,
			Private: repo.IsPrivate, ProjectsEnabled: repoProjectsEnabled(ctx, repo),
		}},
		Projects: make([]ProjectsPickerProject, 0, len(projects)),
	}
	for _, p := range projects {
		columns, err := project_model.CountColumns(ctx, p.ID)
		if err != nil {
			ctx.APIErrorInternal(err)
			return
		}
		out.Projects = append(out.Projects, ProjectsPickerProject{
			ID: p.ID, Title: p.Title, RepoID: p.RepoID, OwnerID: p.OwnerID,
			Type: int(p.Type), IsClosed: p.IsClosed, Columns: columns,
		})
	}
	hubapi.RenderPage(ctx, q, total, out)
}

// listProjectPickerRepos answers the no-repo_id form: every repository the caller can read
// the Issues unit of, and no project — a board is opened only once a repository is chosen.
func listProjectPickerRepos(ctx *context.APIContext, q *query.Query) {
	repos, total, err := repo_model.SearchRepository(ctx, repo_model.SearchRepoOptions{
		ListOptions: db.ListOptions{Page: q.Page, PageSize: q.Limit},
		Actor:       ctx.Doer, Private: true, UnitType: unit.TypeIssues,
	})
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	out := &ProjectsPage{Repos: make([]ProjectsPickerRepo, 0, len(repos)), Projects: []ProjectsPickerProject{}}
	for _, r := range repos {
		out.Repos = append(out.Repos, ProjectsPickerRepo{
			ID: r.ID, FullName: r.FullName(), Owner: r.OwnerName, Name: r.Name,
			Private: r.IsPrivate, ProjectsEnabled: repoProjectsEnabled(ctx, r),
		})
	}
	hubapi.RenderPage(ctx, q, total, out)
}

// equalityFilterBool reads a bare boolean filter, mirroring hubapi's own int/string helpers —
// there is no exported one for KindBool because no other resource has needed it yet.
func equalityFilterBool(q *query.Query, name string) optional.Option[bool] {
	if v, ok := hubapi.EqualityFilter(q, name); ok {
		if b, isBool := v.(bool); isBool {
			return optional.Some(b)
		}
	}
	return optional.None[bool]()
}

// ProjectViewRow is one saved view as the endpoints publish it.
type ProjectViewRow struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	Name        string `json:"name"`
	Query       string `json:"query"`
	CreatedBy   int64  `json:"created_by"`
	CreatedUnix int64  `json:"created_unix"`
}

// ProjectViewList is every write's response: the view list as it now stands, so a caller
// never has to re-fetch to see what its own write did.
type ProjectViewList struct {
	Views []ProjectViewRow `json:"views"`
}

func projectViewRowFrom(row *planning_model.ProjectView) ProjectViewRow {
	return ProjectViewRow{
		ID: row.ID, ProjectID: row.ProjectID, Name: row.Name, Query: row.Query,
		CreatedBy: row.CreatedBy, CreatedUnix: int64(row.CreatedUnix),
	}
}

var projectViewPathParams = []hubapi.Param{
	{Name: "project_id", In: "path", Type: "integer", Required: true, Description: "The project the view is saved on."},
}

var projectViewQueryParams = []hubapi.Param{
	{Name: "repo", In: "query", Type: "string", Required: true, Description: "Repository as owner/name; the project must belong to it, or to its owning organization."},
}

var projectViewSaveBody = []hubapi.Param{
	{Name: "repo", In: "body", Type: "string", Required: true, Description: "Repository as owner/name; the project must belong to it, or to its owning organization."},
	{Name: "name", In: "body", Type: "string", Required: true, Description: "The view's name, 1-100 characters, unique on the project."},
	{Name: "query", In: "body", Type: "string", Description: "The saved query string, at most 4KiB."},
}

var projectViewDeleteBody = []hubapi.Param{
	{Name: "repo", In: "body", Type: "string", Required: true, Description: "Repository as owner/name; the project must belong to it, or to its owning organization."},
}

func getProjectViewsEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getProjectViews", Method: http.MethodGet, Path: "/projects/{project_id}/views",
			Summary:     "A project's saved views",
			Description: "Needs Projects unit read on the named repository. The project must belong to that repository, or to its owning organization, else project_not_in_repo.",
			Tag:         "projects", PathParams: projectViewPathParams, QueryParams: projectViewQueryParams,
			CLINames: []string{"project-views"},
			Response: "ProjectViewList", ResponseIs: "object",
		},
		Handler: GetProjectViews,
	}
}

func createProjectViewEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "createProjectView", Method: http.MethodPost, Path: "/projects/{project_id}/views",
			Summary:     "Save a view on a project",
			Description: "Needs Projects unit write on the named repository. Refused with view_exists when the project already carries a view of that name.",
			Tag:         "projects", PathParams: projectViewPathParams, Body: projectViewSaveBody,
			CLINames: []string{"project-view-save"},
			Response: "ProjectViewList", ResponseIs: "object",
		},
		Handler: CreateProjectView,
	}
}

func deleteProjectViewEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "deleteProjectView", Method: http.MethodDelete, Path: "/projects/{project_id}/views/{view_id}",
			Summary: "Delete a saved view",
			Description: "Needs Projects unit write on the named repository. view_not_found when the view belongs to a " +
				"different project than project_id names.",
			Tag: "projects", PathParams: append(append([]hubapi.Param{}, projectViewPathParams...),
				hubapi.Param{Name: "view_id", In: "path", Type: "integer", Required: true, Description: "The view to delete."}),
			Body:     projectViewDeleteBody,
			CLINames: []string{"project-view-delete"},
			Response: "ProjectViewList", ResponseIs: "object",
		},
		Handler: DeleteProjectView,
	}
}

// projectViewRepo resolves repoName, checks it is visible and carries the Projects unit
// access this operation needs, and loads the project — refusing one that does not belong to
// the repository or its owning organization.
func projectViewRepo(ctx *context.APIContext, projectID int64, repoName string, needWrite bool) (*project_model.Project, bool) {
	owner, name, found := strings.Cut(strings.TrimSpace(repoName), "/")
	if !found || owner == "" || name == "" {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_repo",
			"repo must be owner/name, got "+repoName,
			"Send repo as owner/name, for example \"acme/widgets\".")
		return nil, false
	}
	repo, err := repo_model.GetRepositoryByOwnerAndName(ctx, owner, name)
	if err != nil {
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository "+owner+"/"+name+" is visible to you",
			"Check the owner and repository name.")
		return nil, false
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}
	if !boardReadable(ctx, perm) {
		return nil, false
	}
	if needWrite {
		if !perm.CanWrite(unit.TypeProjects) {
			hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
				"your account has no write access to the Projects unit of "+repo.FullName(),
				"Ask a repository administrator for write permission on that unit.")
			return nil, false
		}
	} else if !perm.CanRead(unit.TypeProjects) {
		hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
			"your account cannot read the Projects unit of "+repo.FullName(),
			"Ask a repository administrator for read access.")
		return nil, false
	}

	project, err := project_model.GetProjectByID(ctx, projectID)
	if err != nil || !projectBelongsToRepo(project, repo) {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "project_not_in_repo",
			"no project with that id belongs to "+repo.FullName(),
			"Check project_id against "+BasePath+"/projects?repo_id="+strings.TrimSpace(repo.FullName())+".")
		return nil, false
	}
	return project, true
}

// projectBelongsToRepo is the ownership check every saved-view endpoint enforces: a
// repository project belonging to repo itself, or an organization project belonging to
// repo's own owning organization.
func projectBelongsToRepo(project *project_model.Project, repo *repo_model.Repository) bool {
	if project.Type == project_model.TypeRepository && project.RepoID == repo.ID {
		return true
	}
	return project.Type == project_model.TypeOrganization && project.OwnerID == repo.OwnerID
}

// renderProjectViews answers with the project's view list as it now stands.
func renderProjectViews(ctx *context.APIContext, projectID int64) {
	rows, err := planning_model.ListProjectViews(ctx, projectID)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	out := &ProjectViewList{Views: make([]ProjectViewRow, 0, len(rows))}
	for _, row := range rows {
		out.Views = append(out.Views, projectViewRowFrom(row))
	}
	ctx.JSON(http.StatusOK, out)
}

// GetProjectViews answers GET /projects/{project_id}/views.
func GetProjectViews(ctx *context.APIContext) {
	projectID := ctx.PathParamInt64("project_id")
	if _, ok := projectViewRepo(ctx, projectID, ctx.FormString("repo"), false); !ok {
		return
	}
	renderProjectViews(ctx, projectID)
}

// projectViewBody is the shape POST and DELETE take: repo always, name and query only for
// the save.
type projectViewBody struct {
	Repo  string `json:"repo"`
	Name  string `json:"name"`
	Query string `json:"query"`
}

func readProjectViewBody(ctx *context.APIContext) (*projectViewBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(ctx.Req.Body, maxProjectViewBody+1))
	if err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "unreadable_body",
			"the request body could not be read", "Retry the request; the body was cut short.")
		return nil, false
	}
	if len(raw) > maxProjectViewBody {
		hubapi.APIError(ctx, http.StatusBadRequest, "body_too_large",
			"the request body is larger than 8KiB",
			"A saved view carries a repo, a name and a query; send only those fields.")
		return nil, false
	}
	body := new(projectViewBody)
	if err := json.Unmarshal(raw, body); err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"repo": "owner/name", "name": "my view", "query": "state:open"}.`)
		return nil, false
	}
	return body, true
}

// CreateProjectView answers POST /projects/{project_id}/views.
func CreateProjectView(ctx *context.APIContext) {
	projectID := ctx.PathParamInt64("project_id")
	body, ok := readProjectViewBody(ctx)
	if !ok {
		return
	}
	project, ok := projectViewRepo(ctx, projectID, body.Repo, true)
	if !ok {
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > maxViewNameLen {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "bad_view_name",
			"a view's name must be 1-100 characters",
			"Give the view a short, non-empty name.")
		return
	}
	if len(body.Query) > maxViewQueryLen {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "bad_query",
			"a view's query must be at most 4KiB",
			"Shorten the saved query.")
		return
	}
	exists, err := planning_model.ProjectViewNameExists(ctx, project.ID, name)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if exists {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "view_exists",
			"a view named "+name+" already exists on this project",
			"Choose a different name, or delete the existing view first.")
		return
	}

	row := &planning_model.ProjectView{ProjectID: project.ID, Name: name, Query: body.Query, CreatedBy: ctx.Doer.ID}
	if err := planning_model.InsertProjectView(ctx, row); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	renderProjectViews(ctx, project.ID)
}

// DeleteProjectView answers DELETE /projects/{project_id}/views/{view_id}.
func DeleteProjectView(ctx *context.APIContext) {
	projectID := ctx.PathParamInt64("project_id")
	body, ok := readProjectViewBody(ctx)
	if !ok {
		return
	}
	project, ok := projectViewRepo(ctx, projectID, body.Repo, true)
	if !ok {
		return
	}

	viewID := ctx.PathParamInt64("view_id")
	view, err := planning_model.GetProjectView(ctx, viewID)
	if err != nil || view.ProjectID != project.ID {
		hubapi.APIError(ctx, http.StatusNotFound, "view_not_found",
			"no view with that id belongs to this project",
			"List the project's own views first: "+BasePath+"/projects/"+strings.TrimSpace(ctx.PathParam("project_id"))+"/views.")
		return
	}
	if err := planning_model.DeleteProjectView(ctx, view.ID); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	renderProjectViews(ctx, project.ID)
}
