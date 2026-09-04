// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"
	"gitea.dev/modules/log"

	"xorm.io/builder"
)

func init() {
	RegisterMigration(&Migration{
		ID:          6,
		Description: "convert epic:<value> labels into plan_issue_parent rows",
		Migrate:     migrateHierarchy,
	})
}

// epicLabelRow is one epic:<value> label carried by one issue.
type epicLabelRow struct {
	IssueID int64
	LabelID int64
	Name    string
}

// digitsOnly is what makes an epic:<value> label the numeric convention rather than the
// named-epic-issue one.
var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

// migrateHierarchy reads only epic:<value> labels and converts each labelled issue's winning
// one into a plan_issue_parent row, the same "first label by id wins, rerun inserts nothing"
// shape migration 5 uses for type: labels.
func migrateHierarchy(ctx context.Context, e db.Engine) error {
	rows := make([]*epicLabelRow, 0, 64)
	if err := e.Table("issue_label").
		Select("issue_label.issue_id AS issue_id, label.id AS label_id, label.name AS name").
		Join("INNER", "label", "label.id = issue_label.label_id").
		Join("INNER", "issue", "issue.id = issue_label.issue_id").
		Where(builder.Expr("LOWER(label.name) LIKE ?", "epic:%")).
		And("issue.is_pull = ?", false).
		OrderBy("issue_label.issue_id ASC, label.id ASC").
		Find(&rows); err != nil {
		return err
	}

	firstOf := map[int64]*epicLabelRow{}
	order := make([]int64, 0, len(rows))
	for _, row := range rows {
		if _, seen := firstOf[row.IssueID]; seen {
			continue
		}
		firstOf[row.IssueID] = row
		order = append(order, row.IssueID)
	}

	counts := map[string]int{}
	for _, issueID := range order {
		reason, err := convertEpicLabel(e, issueID, firstOf[issueID])
		if err != nil {
			return err
		}
		counts[reason]++
	}
	log.Info("hub: hierarchy migration converted %d epic label(s) into a parent; skipped %d already linked, %d self, %d not found, %d ambiguous, %d rank-refused",
		counts["converted"], counts["already_linked"], counts["self"], counts["not_found"], counts["ambiguous"], counts["rank_refused"])
	return nil
}

// convertEpicLabel resolves one issue's winning epic: label into a parent row, or names why it
// was skipped instead.
func convertEpicLabel(e db.Engine, issueID int64, label *epicLabelRow) (string, error) {
	has, err := e.Where("child_issue_id = ?", issueID).Exist(new(planning_model.IssueParent))
	if err != nil || has {
		return "already_linked", err
	}

	value := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(label.Name), "epic:"))
	if value == "" {
		return "not_found", nil
	}

	issue := new(issues_model.Issue)
	hasIssue, err := e.ID(issueID).Get(issue)
	if err != nil || !hasIssue {
		return "not_found", err
	}

	var parentID int64
	if digitsOnly.MatchString(value) {
		index, convErr := strconv.ParseInt(value, 10, 64)
		if convErr != nil {
			return "not_found", nil
		}
		parentID, err = issueIDByIndex(e, issue.RepoID, index)
	} else {
		parentID, err = epicIssueIDByLabel(e, issue.RepoID, value)
	}
	if err != nil {
		return "", err
	}
	switch parentID {
	case 0:
		return "not_found", nil
	case -1:
		return "ambiguous", nil
	case issueID:
		return "self", nil
	}

	parentRank, parentTyped, err := issueTypeRank(e, parentID)
	if err != nil {
		return "", err
	}
	childRank, childTyped, err := issueTypeRank(e, issueID)
	if err != nil {
		return "", err
	}
	if !parentTyped || !childTyped || !(parentRank < childRank) {
		return "rank_refused", nil
	}

	if _, err := e.Insert(&planning_model.IssueParent{ChildIssueID: issueID, ParentIssueID: parentID}); err != nil {
		return "", err
	}
	return "converted", nil
}

// issueIDByIndex resolves the numeric convention: the issue with that per-repository index.
// Pull requests are excluded: hierarchy links issues only.
func issueIDByIndex(e db.Engine, repoID, index int64) (int64, error) {
	issue := new(issues_model.Issue)
	has, err := e.Where(builder.Eq{"repo_id": repoID, "`index`": index, "is_pull": false}).Get(issue)
	if err != nil || !has {
		return 0, err
	}
	return issue.ID, nil
}

// epicIssueIDByLabel resolves the named convention: the issue in repoID assigned the type
// named epic that itself carries the label epic:<value> exactly — how the epic issue names
// itself. Pull requests are excluded, and the match is exact rather than a substring LIKE, so
// a shorter value never resolves to a longer label that merely contains it. 0 means none found,
// -1 means more than one candidate.
func epicIssueIDByLabel(e db.Engine, repoID int64, value string) (int64, error) {
	repoScopeID, orgID, err := repoScope(e, repoID)
	if err != nil {
		return 0, err
	}
	typeID, err := visibleTypeIDNamed(e, repoScopeID, orgID, "epic")
	if err != nil || typeID == 0 {
		return 0, err
	}

	ids := make([]int64, 0, 2)
	if err := e.Table("plan_issue_type_assignment").
		Select("plan_issue_type_assignment.issue_id").
		Join("INNER", "issue", "issue.id = plan_issue_type_assignment.issue_id").
		Join("INNER", "issue_label", "issue_label.issue_id = issue.id").
		Join("INNER", "label", "label.id = issue_label.label_id").
		Where("plan_issue_type_assignment.type_id = ? AND issue.repo_id = ? AND issue.is_pull = ?", typeID, repoID, false).
		And(builder.Expr("LOWER(label.name) = ?", "epic:"+value)).
		Find(&ids); err != nil {
		return 0, err
	}

	uniq := map[int64]bool{}
	for _, id := range ids {
		uniq[id] = true
	}
	switch len(uniq) {
	case 0:
		return 0, nil
	case 1:
		for id := range uniq {
			return id, nil
		}
	}
	return -1, nil
}

// issueTypeRank reads issueID's assigned type's rank, ok=false when it carries none.
func issueTypeRank(e db.Engine, issueID int64) (int, bool, error) {
	assignment := new(planning_model.IssueTypeAssignment)
	has, err := e.Where("issue_id = ?", issueID).Get(assignment)
	if err != nil || !has {
		return 0, false, err
	}
	t := new(planning_model.IssueType)
	has, err = e.ID(assignment.TypeID).Get(t)
	if err != nil || !has {
		return 0, false, err
	}
	return t.Rank, true, nil
}
