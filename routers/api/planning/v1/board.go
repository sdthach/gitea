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
	project_model "gitea.dev/models/project"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/json"
	"gitea.dev/modules/optional"
	hubapi "gitea.dev/routers/api/hub"
	"gitea.dev/services/context"
	"gitea.dev/services/hub/query"
	issue_service "gitea.dev/services/issue"
	planning_service "gitea.dev/services/planning"
	project_service "gitea.dev/services/projects"
)

// maxBoardMoveBody caps a board write's body. A card move carries ids and a group name; it is
// never an upload.
const maxBoardMoveBody = 16 << 10

// boardSpec is the board projection's whitelist declaration.
//
// The board is a PROJECTION over Gitea's own project rows, not a table of its own, so its
// parameters select what to project rather than rendering into a SQL condition. They still
// go through the one grammar, so an unknown parameter is a 400 that names the offender
// rather than a silently ignored word.
var boardSpec = query.Spec{
	Resource: "board",
	Fields: []query.Field{
		{Name: "repo_id", Column: "repo_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "project_id", Column: "project_id", Kind: query.KindInt, Ops: []query.Op{query.OpEq}},
		{Name: "group_by", Column: "group_by", Kind: query.KindString, Ops: []query.Op{query.OpEq}},
	},
	PrimaryKey: "project_id",
	Paging:     query.PagingOffset,
}

// Board is the board resource's response shape: the columns Gitea models and the groups it
// does not.
type Board struct {
	RepoID       int64                          `json:"repo_id"`
	RepoFullName string                         `json:"repo_full_name"`
	ProjectID    int64                          `json:"project_id"`
	Title        string                         `json:"title"`
	GroupBy      string                         `json:"group_by"`
	Columns      []planning_service.BoardColumn `json:"columns"`
	Groups       []planning_service.Group       `json:"groups"`
	// Tree is every recorded parent edge among this board's cards' repository, so a client
	// can draw the hierarchy without re-deriving it from each card's own parent_issue_id.
	Tree []planning_service.TreeEdge `json:"tree"`
	// Types are the types visible from this repository, what a card's type picker offers.
	Types []planning_service.VisibleType `json:"types"`
	// CanWrite is whether the calling user may perform either of the two writes, resolved
	// by the same checks the write endpoints enforce, so the page offers no action it
	// would be refused for.
	CanWrite     bool `json:"can_write"`
	CanEditIssue bool `json:"can_edit_issue"`
}

func getBoardEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "getBoard", Method: http.MethodGet, Path: "/board",
			Summary: "A project board with horizontal groups",
			Description: "Gitea's project columns rendered vertically, with horizontal groups it does not model at all: " +
				"project.Column carries title, sorting, colour and project and no group or row field, so groups are a " +
				"rendering over rows the Projects API already returns — no schema change and no fork change. " +
				"group_by selects the group dimension at view time and is never stored on the project, so two people may " +
				"group the same board differently. An issue with no value for the active grouping falls into an " +
				"explicit empty-value group; nothing disappears from a board because a field is unset. " +
				"Board support is probed at runtime: a repository whose Projects unit is disabled, or an instance that " +
				"disables it globally, is answered with the reason and what to do about it rather than an empty board. " +
				"The /planning/board page is a client of this endpoint.",
			Tag: "board", Query: &boardSpec, Response: "Board", ResponseIs: "object",
		},
		Handler: GetBoard,
	}
}

var boardCardParams = []hubapi.Param{
	{
		Name: "issue_id", In: "path", Type: "integer", Required: true,
		Description: "The issue's GLOBAL id, which is what Gitea's own project endpoints address a card by — not its per-repository number.",
	},
}

var boardColumnMoveParams = []hubapi.Param{
	{Name: "repo", In: "body", Type: "string", Required: true, Description: "Repository as owner/name."},
	{Name: "project_id", In: "body", Type: "integer", Required: true, Description: "The board the card is on."},
	{Name: "column_id", In: "body", Type: "integer", Required: true, Description: "The column to move the card into."},
	{Name: "sorting", In: "body", Type: "integer", Description: "Position within the column. Omit to append."},
}

var boardGroupMoveParams = []hubapi.Param{
	{Name: "repo", In: "body", Type: "string", Required: true, Description: "Repository as owner/name."},
	{Name: "project_id", In: "body", Type: "integer", Required: true, Description: "The board the card is on."},
	{
		Name: "group_by", In: "body", Type: "string", Required: true, Enum: planning_service.Groupings,
		Description: "The active grouping. A group move is refused when this is none, because there is nothing to write.",
	},
	{
		Name: "group", In: "body", Type: "string",
		Description: "The group's key: the type name, the assignee login, or — under parent grouping — the root issue's id as a string. Empty moves the card into the empty-value group, clearing the field.",
	},
}

func moveBoardCardColumnEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "moveBoardCardColumn", Method: http.MethodPost, Path: "/board/cards/{issue_id}/column",
			Summary: "Move a card between the board's columns",
			Description: "The first of the board's exactly two writes. It is the same operation ccpm's board-issue-move " +
				"verb performs over Gitea's own Projects API, reached here as the calling user. " +
				"Authorized by Gitea's own write check on the Projects unit.",
			Tag: "board", PathParams: boardCardParams, Body: boardColumnMoveParams,
			CLINames: []string{"board-move-column"},
			Response: "Board", ResponseIs: "object",
		},
		Handler: MoveBoardCardColumn,
	}
}

func moveBoardCardGroupEndpoint() *hubapi.Endpoint {
	return &hubapi.Endpoint{
		Op: &hubapi.Operation{
			ID: "moveBoardCardGroup", Method: http.MethodPost, Path: "/board/cards/{issue_id}/group",
			Summary: "Move a card between the board's groups",
			Description: "The second and last of the board's writes. A group IS the grouping value, so moving between " +
				"groups edits the field itself: the issue's assigned type, its recorded parent, or the assignee. " +
				"It is REFUSED when grouping is off, because there is then nothing to write, and the refusal says so. " +
				"Authorized by Gitea's own write check on the Issues unit.",
			Tag: "board", PathParams: boardCardParams, Body: boardGroupMoveParams,
			CLINames: []string{"board-move-group"},
			Response: "Board", ResponseIs: "object",
		},
		Handler: MoveBoardCardGroup,
	}
}

// boardAvailable is the runtime probe for Projects availability, on the fork's side.
//
// The fork cannot lose its own compiled-in Projects API, so the runtime condition it CAN
// meet is the one Gitea itself enforces before serving a board: the Projects unit disabled
// instance-wide, or repo-level boards disallowed on this repository. Either answers with the
// reason and what to do rather than an empty board that reads as "no cards".
func boardAvailable(ctx *context.APIContext, repo *repo_model.Repository) bool {
	if unit.TypeProjects.UnitGlobalDisabled() {
		hubapi.APIError(ctx, http.StatusNotFound, "projects_unavailable",
			"this instance disables the Projects unit, so it serves no boards",
			"Remove `projects` from DISABLED_REPO_UNITS in app.ini, or use "+BasePath+"/roadmap, which needs no Projects API.")
		return false
	}
	projectsUnit, err := repo.GetUnit(ctx, unit.TypeProjects)
	if err != nil || !projectsUnit.ProjectsConfig().IsProjectsAllowed(repo_model.ProjectsModeRepo) {
		hubapi.APIError(ctx, http.StatusNotFound, "projects_unavailable",
			"the Projects unit is not enabled for repository-level boards on "+repo.FullName(),
			"Enable Projects for this repository under its settings, or use "+BasePath+"/roadmap, which needs no Projects API.")
		return false
	}
	return true
}

// boardRepo resolves and authorizes the repository a board request names.
func boardRepo(ctx *context.APIContext, repoID int64) (*repo_model.Repository, access.Permission, bool) {
	var zero access.Permission
	if repoID <= 0 {
		hubapi.APIError(ctx, http.StatusBadRequest, "missing_repo_id",
			"repo_id is required: a board belongs to a repository",
			"Pass ?repo_id=<id>, listing "+BasePath+"/repos to find it.")
		return nil, zero, false
	}
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository with id is visible to you",
			"Check the id against "+BasePath+"/repos.")
		return nil, zero, false
	}
	perm, err := access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, zero, false
	}
	if !boardReadable(ctx, perm) {
		return nil, zero, false
	}
	return repo, perm, true
}

// boardReadable is the read gate and it runs FIRST, before boardAvailable: answering "this
// repository has no Projects unit" to a caller who cannot see the repository at all would
// disclose its configuration to them.
//
// It checks visibility and nothing else. perm.CanRead(unit.TypeProjects) cannot stand here,
// because it is false both for "you may not read this unit" and for "this repository has no
// such unit" — conflating them is what makes a degraded board report itself as a permission
// problem. The unit-read check is boardProjectsReadable, after availability has excluded the
// second meaning.
//
// A caller with no unit access is answered 404, not 403, so a private repository's existence
// stays hidden.
func boardReadable(ctx *context.APIContext, perm access.Permission) bool {
	if !perm.HasAnyUnitAccess() {
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository with that id is visible to you",
			"Check the id against "+BasePath+"/repos.")
		return false
	}
	return true
}

// boardProjectsReadable is a fail-CLOSED backstop, and it runs only after boardAvailable has
// established that the repository does have a Projects unit — so here a false answer can
// only mean "you may not read it".
//
// No request reaches its refusal on this tree, and that is measured rather than assumed:
// models/organization/team.go:214 raises every unit's mode to the team's own AccessMode
// (`mode = max(mode, t.AccessMode)`), and Permission.UnitAccessMode
// (models/perm/access/repo_permission.go:99-105) falls back to Permission.AccessMode for any
// unit the repository actually has. A caller who has passed HasAnyUnitAccess can therefore
// always read the Projects unit of a repository that has one. It is kept rather than deleted
// because it is the only thing that would hold if Gitea gained a real per-unit read
// restriction — and it is marked here so nobody reads it as a gate doing work today.
func boardProjectsReadable(ctx *context.APIContext, repo *repo_model.Repository, perm access.Permission) bool {
	if !perm.CanRead(unit.TypeProjects) {
		hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
			"your account cannot read the Projects unit of "+repo.FullName(),
			"Ask a repository administrator for read access, or use "+BasePath+"/roadmap, which reads issues instead.")
		return false
	}
	return true
}

// boardProject loads a board and refuses one that belongs to another repository. An
// owner-level board spans repositories and would need a per-issue permission check on every
// card; this endpoint serves repository boards, which is what an epic sync creates.
func boardProject(ctx *context.APIContext, repo *repo_model.Repository, projectID int64) (*project_model.Project, bool) {
	if projectID <= 0 {
		hubapi.APIError(ctx, http.StatusBadRequest, "missing_project_id",
			"project_id is required: the board to render",
			"Pass ?project_id=<id>. An epic sync records it as .board.number in the epic's sync-manifest.json.")
		return nil, false
	}
	project, err := project_model.GetProjectByID(ctx, projectID)
	if err != nil || project.RepoID != repo.ID {
		hubapi.APIError(ctx, http.StatusNotFound, "board_not_found",
			"no repository board with that id belongs to "+repo.FullName(),
			"Check project_id, and note that owner-level and user-level boards are not served here: they span repositories.")
		return nil, false
	}
	return project, true
}

// readBoard assembles the board: the project's columns, its cards, and the groups those cards
// fall into under the requested grouping.
func readBoard(ctx *context.APIContext, repo *repo_model.Repository, perm access.Permission, project *project_model.Project, grouping planning_service.Grouping) (*Board, bool) {
	columns, err := project_model.GetColumns(ctx, project.ID, db.ListOptions{ListAll: true})
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}

	defaultColumnID := int64(0)
	out := make([]planning_service.BoardColumn, 0, len(columns))
	for _, col := range columns {
		out = append(out, planning_service.BoardColumn{ID: col.ID, Title: col.Title, Color: col.Color, Default: col.Default})
		if col.Default {
			defaultColumnID = col.ID
		}
	}
	if defaultColumnID == 0 && len(out) > 0 {
		defaultColumnID = out[0].ID
	}

	rows := make([]*project_model.ProjectIssue, 0, 32)
	if err := db.GetEngine(ctx).Where("project_id=?", project.ID).OrderBy("sorting, id").Find(&rows); err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}

	placement := make(map[int64]*project_model.ProjectIssue, len(rows))
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		placement[row.IssueID] = row
		ids = append(ids, row.IssueID)
	}

	issues, err := issues_model.GetIssuesByIDs(ctx, ids)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}
	if err := issues.LoadAttributes(ctx); err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}

	assigned, err := planning_service.Assignments(ctx, ids)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}

	parents, err := planning_service.ParentMap(ctx, repo.ID)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}
	depths := planning_service.Depths(parents)
	hasChildren := make(map[int64]bool, len(parents))
	for _, parentID := range parents {
		hasChildren[parentID] = true
	}

	cards := make([]planning_service.Card, 0, len(issues))
	for _, issue := range issues {
		row := placement[issue.ID]
		if row == nil {
			continue
		}
		columnID := row.ProjectColumnID
		// Legacy rows carry a zero column and render in the default one
		// (models/project/issue.go:20-21). Dropping them would lose cards silently.
		if columnID == 0 {
			columnID = defaultColumnID
		}
		card := planning_service.Card{
			IssueID: issue.ID, Number: issue.Index, Title: issue.Title,
			URL:      issue.Link(),
			ColumnID: columnID, Sorting: row.Sorting,
			IsClosed: issue.IsClosed, IsPull: issue.IsPull,
			ParentIssueID: parents[issue.ID], RootIssueID: planning_service.RootOf(parents, issue.ID),
			Depth: depths[issue.ID], HasChildren: hasChildren[issue.ID],
		}
		if at, ok := assigned[issue.ID]; ok {
			card.Type, card.TypeID, card.TypeColor, card.TypeIcon = at.Name, at.TypeID, at.Color, at.Icon
		}
		for _, label := range issue.Labels {
			card.Labels = append(card.Labels, label.Name)
		}
		for _, assignee := range issue.Assignees {
			card.Assignees = append(card.Assignees, assignee.Name)
		}
		cards = append(cards, card)
	}

	types, err := planning_service.TypesFor(ctx, repo)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, false
	}

	groups := planning_service.BuildGroups(out, cards, grouping)
	// A parent-grouped row's root may not be one of this board's own cards — off the board
	// entirely, or on it under a column this fetch already saw but with no title resolved yet
	// — so its title is fetched here, the one place this projection touches the database for it.
	if missing := planning_service.GroupsMissingRootTitle(groups); len(missing) > 0 {
		roots, err := issues_model.GetIssuesByIDs(ctx, missing)
		if err != nil {
			ctx.APIErrorInternal(err)
			return nil, false
		}
		titles := make(map[int64]string, len(roots))
		for _, root := range roots {
			titles[root.ID] = root.Title
		}
		planning_service.ApplyRootTitles(groups, titles)
	}

	return &Board{
		RepoID: repo.ID, RepoFullName: repo.FullName(),
		ProjectID: project.ID, Title: project.Title, GroupBy: string(grouping),
		Columns:      out,
		Groups:       groups,
		Tree:         planning_service.BuildTree(parents),
		Types:        types,
		CanWrite:     perm.CanWrite(unit.TypeProjects),
		CanEditIssue: perm.CanWrite(unit.TypeIssues),
	}, true
}

// GetBoard answers GET /board.
func GetBoard(ctx *context.APIContext) {
	q, ok := hubapi.ParseQuery(ctx, boardSpec)
	if !ok {
		return
	}
	grouping, ok := parseGrouping(ctx, hubapi.EqualityFilterString(q, "group_by"))
	if !ok {
		return
	}
	repo, perm, ok := boardRepo(ctx, hubapi.EqualityFilterInt(q, "repo_id"))
	if !ok {
		return
	}
	if !boardAvailable(ctx, repo) {
		return
	}
	if !boardProjectsReadable(ctx, repo, perm) {
		return
	}
	project, ok := boardProject(ctx, repo, hubapi.EqualityFilterInt(q, "project_id"))
	if !ok {
		return
	}
	board, ok := readBoard(ctx, repo, perm, project, grouping)
	if !ok {
		return
	}
	ctx.JSON(http.StatusOK, board)
}

// parseGrouping refuses an unknown grouping naming what is accepted, rather than falling
// back to none and rendering a board the caller did not ask for.
func parseGrouping(ctx *context.APIContext, raw string) (planning_service.Grouping, bool) {
	grouping, ok := planning_service.ParseGrouping(raw)
	if !ok {
		ctx.JSON(http.StatusBadRequest, &query.Error{
			Status: http.StatusBadRequest, Code: "unknown_grouping",
			Message:         "no such group grouping: " + raw,
			Parameter:       "group_by",
			Accepted:        planning_service.Groupings,
			SuggestedAction: "Group by one of " + strings.Join(planning_service.Groupings, ", ") + ", or omit group_by for Gitea's own single-group board.",
		})
		return planning_service.GroupNone, false
	}
	return grouping, true
}

// boardMoveBody is the shared shape of the board's two writes.
type boardMoveBody struct {
	Repo      string `json:"repo"`
	ProjectID int64  `json:"project_id"`
	ColumnID  int64  `json:"column_id"`
	Sorting   *int64 `json:"sorting"`
	GroupBy   string `json:"group_by"`
	Group     string `json:"group"`
}

// readBoardMoveBody reads a board write's body under maxBoardMoveBody.
func readBoardMoveBody(ctx *context.APIContext) (*boardMoveBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(ctx.Req.Body, maxBoardMoveBody+1))
	if err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "unreadable_body",
			"the request body could not be read", "Retry the request; the body was cut short.")
		return nil, false
	}
	if len(raw) > maxBoardMoveBody {
		hubapi.APIError(ctx, http.StatusBadRequest, "body_too_large",
			"the request body is larger than 16KiB",
			"A card move carries ids and a group name; send only those fields.")
		return nil, false
	}
	body := new(boardMoveBody)
	if err := json.Unmarshal(raw, body); err != nil {
		hubapi.APIError(ctx, http.StatusBadRequest, "malformed_body",
			"the request body is not the JSON object this endpoint takes",
			`Send {"repo": "owner/name", "project_id": 5, "column_id": 11} to move a column, or `+
				`{"repo": "owner/name", "project_id": 5, "group_by": "type", "group": "bug"} to move a group.`)
		return nil, false
	}
	return body, true
}

// boardMoveTarget resolves the repository, the board, the card and the permission every
// board write needs. It is the one place both writes are authorized.
func boardMoveTarget(ctx *context.APIContext, needed unit.Type) (*boardMoveBody, *repo_model.Repository, access.Permission, *project_model.Project, *issues_model.Issue, bool) {
	var perm access.Permission
	body, ok := readBoardMoveBody(ctx)
	if !ok {
		return nil, nil, perm, nil, nil, false
	}

	owner, name, found := strings.Cut(strings.TrimSpace(body.Repo), "/")
	if !found || owner == "" || name == "" {
		hubapi.APIError(ctx, http.StatusBadRequest, "bad_repo",
			"repo must be owner/name, got "+body.Repo,
			"Send repo as owner/name, for example \"acme/widgets\".")
		return nil, nil, perm, nil, nil, false
	}
	repo, err := repo_model.GetRepositoryByOwnerAndName(ctx, owner, name)
	if err != nil {
		hubapi.APIError(ctx, http.StatusNotFound, "repo_not_found",
			"no repository "+owner+"/"+name+" is visible to you",
			"Check the owner and repository name against "+BasePath+"/repos.")
		return nil, nil, perm, nil, nil, false
	}
	perm, err = access.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, nil, perm, nil, nil, false
	}
	// Visibility first, so a caller who cannot see the repository is not told how its
	// Projects unit is configured; then availability, so an absent unit is never reported
	// as a permission problem; then the write permission itself.
	if !boardReadable(ctx, perm) {
		return nil, nil, perm, nil, nil, false
	}
	if !boardAvailable(ctx, repo) {
		return nil, nil, perm, nil, nil, false
	}
	if !boardProjectsReadable(ctx, repo, perm) {
		return nil, nil, perm, nil, nil, false
	}
	if !perm.CanWrite(needed) {
		hubapi.APIError(ctx, http.StatusForbidden, "forbidden",
			"your account has no write access to the "+needed.LogString()+" unit of "+repo.FullName(),
			"Ask a repository administrator for write permission on that unit.")
		return nil, nil, perm, nil, nil, false
	}
	project, ok := boardProject(ctx, repo, body.ProjectID)
	if !ok {
		return nil, nil, perm, nil, nil, false
	}

	issue, err := issues_model.GetIssueByRepoID(ctx, repo.ID, ctx.PathParamInt64("issue_id"))
	if err != nil {
		hubapi.APIError(ctx, http.StatusNotFound, "card_not_found",
			"no issue with that id belongs to "+repo.FullName(),
			"The path takes the issue's global id, not its per-repository number; "+BasePath+"/board lists issue_id for every card.")
		return nil, nil, perm, nil, nil, false
	}
	return body, repo, perm, project, issue, true
}

// MoveBoardCardColumn answers POST /board/cards/{issue_id}/column — the first of the two
// writes. It reaches Gitea's own MoveIssueToColumn, so a card moved here and a card moved on
// Gitea's board land in the same place by the same code.
func MoveBoardCardColumn(ctx *context.APIContext) {
	body, repo, perm, project, issue, ok := boardMoveTarget(ctx, unit.TypeProjects)
	if !ok {
		return
	}
	column, err := project_model.GetColumnByIDAndProjectID(ctx, body.ColumnID, project.ID)
	if err != nil {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "column_not_found",
			"no column with that id belongs to this board",
			"Read the board's columns from "+BasePath+"/board and use one of their column_id values.")
		return
	}
	if err := project_service.MoveIssueToColumn(ctx, ctx.Doer, issue, column, optional.FromPtr(body.Sorting)); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	renderBoardAfterWrite(ctx, repo, perm, project, body.GroupBy)
}

// MoveBoardCardGroup answers POST /board/cards/{issue_id}/group — the second and last write.
// A group is the grouping value itself, so this edits the issue's assigned type, its recorded
// parent, or the assignee, and is refused outright when grouping is off.
func MoveBoardCardGroup(ctx *context.APIContext) {
	body, repo, perm, project, issue, ok := boardMoveTarget(ctx, unit.TypeIssues)
	if !ok {
		return
	}
	grouping, ok := parseGrouping(ctx, body.GroupBy)
	if !ok {
		return
	}
	write, err := planning_service.PlanGroupMove(grouping, body.Group)
	if err != nil {
		// The refusal: with grouping off there is no field to write, and the message
		// says which write does still work.
		hubapi.RenderHubError(ctx, http.StatusBadRequest, err)
		return
	}

	if err := issue.LoadRepo(ctx); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	switch write.Kind {
	case planning_service.GroupWriteType:
		if !applyGroupType(ctx, issue, write) {
			return
		}
	case planning_service.GroupWriteParent:
		if !applyGroupParent(ctx, issue, write) {
			return
		}
	case planning_service.GroupWriteAssignee:
		if !applyGroupAssignee(ctx, issue, write) {
			return
		}
	}
	renderBoardAfterWrite(ctx, repo, perm, project, body.GroupBy)
}

// applyGroupParent sets or removes the card's recorded parent through SetIssueParent, which is
// where every hierarchy refusal — same_issue, cross_repo, rank_mismatch, cycle and the rest —
// lives; the board itself enforces none of them a second time.
func applyGroupParent(ctx *context.APIContext, issue *issues_model.Issue, write planning_service.GroupWrite) bool {
	if write.ParentIssueID == 0 {
		if err := planning_service.RemoveIssueParent(ctx, issue.ID); err != nil {
			ctx.APIErrorInternal(err)
			return false
		}
		return true
	}
	parent, err := issues_model.GetIssueByID(ctx, write.ParentIssueID)
	if err != nil {
		hubapi.APIError(ctx, http.StatusUnprocessableEntity, "parent_not_found",
			"no issue with that id exists to be the group's root",
			"Move to one of the group keys the board itself publishes.")
		return false
	}
	if err := planning_service.SetIssueParent(ctx, issue, parent); err != nil {
		hubapi.RenderHubError(ctx, http.StatusUnprocessableEntity, err)
		return false
	}
	return true
}

// applyGroupAssignee makes the target group's user the issue's only assignee, because the group
// a card sits in under assignee grouping is its first assignee.
func applyGroupAssignee(ctx *context.APIContext, issue *issues_model.Issue, write planning_service.GroupWrite) bool {
	if err := issue.LoadAssignees(ctx); err != nil {
		ctx.APIErrorInternal(err)
		return false
	}
	wanted := int64(0)
	if write.Assignee != "" {
		assignee, err := findAssignee(ctx, issue, write.Assignee)
		if err != nil {
			ctx.APIErrorInternal(err)
			return false
		}
		if assignee == 0 {
			hubapi.APIError(ctx, http.StatusUnprocessableEntity, "assignee_not_found",
				"no user named "+write.Assignee+" can be assigned issues in this repository",
				"Move the card to a group whose login is an assignable user, or add the user to the repository first.")
			return false
		}
		wanted = assignee
	}
	for _, current := range issue.Assignees {
		if current.ID == wanted {
			continue
		}
		if _, _, err := issue_service.ToggleAssigneeWithNotify(ctx, issue, ctx.Doer, current.ID); err != nil {
			ctx.APIErrorInternal(err)
			return false
		}
	}
	if wanted == 0 {
		return true
	}
	for _, current := range issue.Assignees {
		if current.ID == wanted {
			return true
		}
	}
	if _, _, err := issue_service.ToggleAssigneeWithNotify(ctx, issue, ctx.Doer, wanted); err != nil {
		ctx.APIErrorInternal(err)
		return false
	}
	return true
}

func findAssignee(ctx *context.APIContext, issue *issues_model.Issue, login string) (int64, error) {
	assignees, err := repo_model.GetRepoAssignees(ctx, issue.Repo)
	if err != nil {
		return 0, err
	}
	for _, user := range assignees {
		if strings.EqualFold(user.Name, login) {
			return user.ID, nil
		}
	}
	return 0, nil
}

// renderBoardAfterWrite answers a write with the board as it now stands, so a client never
// has to guess what its own move produced.
func renderBoardAfterWrite(ctx *context.APIContext, repo *repo_model.Repository, perm access.Permission, project *project_model.Project, groupBy string) {
	grouping, ok := parseGrouping(ctx, groupBy)
	if !ok {
		return
	}
	board, ok := readBoard(ctx, repo, perm, project, grouping)
	if !ok {
		return
	}
	ctx.JSON(http.StatusOK, board)
}
