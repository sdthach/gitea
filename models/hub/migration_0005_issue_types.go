// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"context"
	"strings"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"

	"xorm.io/builder"
)

func init() {
	RegisterMigration(&Migration{
		ID:          5,
		Description: "seed the instance's issue types and convert type: labels into plan_issue_type_assignment rows",
		Migrate:     migrateIssueTypes,
	})
}

// instanceSeedType is one of the six types every instance starts with.
type instanceSeedType struct {
	name, color, icon string
	rank              int
}

// instanceSeedTypes ranks epic above feature above the three same-ranked kinds of work above
// task, with a distinct octicon and colour each so a board or roadmap never has to guess one.
var instanceSeedTypes = []instanceSeedType{
	{"epic", "#8250df", "octicon-rocket", 1},
	{"feature", "#0969da", "octicon-light-bulb", 2},
	{"story", "#2da44e", "octicon-tasklist", 3},
	{"bug", "#d1242f", "octicon-bug", 3},
	{"spike", "#bf8700", "octicon-beaker", 3},
	{"task", "#57606a", "octicon-checklist", 4},
}

// migrateIssueTypes seeds the instance scope's six starting types, then converts every
// type:<name> label into an assignment. Both halves are idempotent on a rerun.
func migrateIssueTypes(ctx context.Context, e db.Engine) error {
	if err := seedInstanceTypes(e); err != nil {
		return err
	}
	return convertTypeLabels(e)
}

// seedInstanceTypes does nothing once the instance scope (0,0) holds any row, so it never
// overwrites types an admin already created there before upgrading.
func seedInstanceTypes(e db.Engine) error {
	has, err := e.Where("repo_id = 0 AND org_id = 0").Exist(new(planning_model.IssueType))
	if err != nil || has {
		return err
	}
	for i, t := range instanceSeedTypes {
		if _, err := e.Insert(&planning_model.IssueType{
			Name: t.name, Color: t.color, Icon: t.icon, Rank: t.rank, Sort: i,
		}); err != nil {
			return err
		}
	}
	return nil
}

// typeLabelRow is one type:<name> label carried by one issue.
type typeLabelRow struct {
	IssueID int64
	LabelID int64
	Name    string
	Color   string
}

// convertTypeLabels reads only type:<name> labels and assigns each labelled issue the matching
// type in its own repo, org, or instance scope. The first label by id wins on an issue with more
// than one.
func convertTypeLabels(e db.Engine) error {
	rows := make([]*typeLabelRow, 0, 64)
	if err := e.Table("issue_label").
		Select("issue_label.issue_id AS issue_id, label.id AS label_id, label.name AS name, label.color AS color").
		Join("INNER", "label", "label.id = issue_label.label_id").
		Where(builder.Expr("LOWER(label.name) LIKE ?", "type:%")).
		OrderBy("issue_label.issue_id ASC, label.id ASC").
		Find(&rows); err != nil {
		return err
	}

	firstOf := map[int64]*typeLabelRow{}
	order := make([]int64, 0, len(rows))
	for _, row := range rows {
		if _, seen := firstOf[row.IssueID]; seen {
			continue
		}
		firstOf[row.IssueID] = row
		order = append(order, row.IssueID)
	}

	for _, issueID := range order {
		if err := assignFromLabel(e, issueID, firstOf[issueID]); err != nil {
			return err
		}
	}
	return nil
}

// assignFromLabel converts one issue's winning type: label into an assignment.
func assignFromLabel(e db.Engine, issueID int64, label *typeLabelRow) error {
	has, err := e.Where("issue_id = ?", issueID).Exist(new(planning_model.IssueTypeAssignment))
	if err != nil || has {
		return err
	}

	name := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(label.Name), "type:"))
	if name == "" {
		return nil
	}

	issue := new(issues_model.Issue)
	hasIssue, err := e.ID(issueID).Get(issue)
	if err != nil || !hasIssue {
		return err
	}

	repoID, orgID, err := repoScope(e, issue.RepoID)
	if err != nil {
		return err
	}

	typeID, err := visibleTypeIDNamed(e, repoID, orgID, name)
	if err != nil {
		return err
	}
	if typeID == 0 {
		created := &planning_model.IssueType{
			RepoID: repoID, Name: name, Color: label.Color, Icon: "octicon-issue-opened", Rank: 3,
		}
		if _, err := e.Insert(created); err != nil {
			return err
		}
		typeID = created.ID
	}

	_, err = e.Insert(&planning_model.IssueTypeAssignment{IssueID: issueID, TypeID: typeID})
	return err
}

// repoScope reads an issue's repository and, when its owner is an organization, that
// organization's id — the two scopes visibleTypeIDNamed shadows an instance type with.
func repoScope(e db.Engine, repoID int64) (int64, int64, error) {
	repo := new(repo_model.Repository)
	hasRepo, err := e.ID(repoID).Get(repo)
	if err != nil || !hasRepo {
		return repoID, 0, err
	}
	owner := new(user_model.User)
	hasOwner, err := e.ID(repo.OwnerID).Get(owner)
	if err != nil || !hasOwner || !owner.IsOrganization() {
		return repoID, 0, err
	}
	return repoID, repo.OwnerID, nil
}

// visibleTypeIDNamed resolves the name the way a repository sees it: its own type first, then
// its organization's, then the instance's — the same shadowing TypesFor applies at read time.
func visibleTypeIDNamed(e db.Engine, repoID, orgID int64, name string) (int64, error) {
	if repoID > 0 {
		if id, ok, err := typeIDInScope(e, repoID, 0, name); err != nil || ok {
			return id, err
		}
	}
	if orgID > 0 {
		if id, ok, err := typeIDInScope(e, 0, orgID, name); err != nil || ok {
			return id, err
		}
	}
	id, _, err := typeIDInScope(e, 0, 0, name)
	return id, err
}

func typeIDInScope(e db.Engine, repoID, orgID int64, name string) (int64, bool, error) {
	row := new(planning_model.IssueType)
	has, err := e.Where("repo_id = ? AND org_id = ? AND name = ?", repoID, orgID, name).Get(row)
	if err != nil || !has {
		return 0, false, err
	}
	return row.ID, true, nil
}
