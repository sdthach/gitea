// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"testing"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertTypeLabel creates a repo-scoped label and attaches it to an issue, returning the
// label's own id — what "first label by id wins" is decided on.
func insertTypeLabel(t *testing.T, repoID, issueID int64, name, color string) int64 {
	t.Helper()
	label := &issues_model.Label{RepoID: repoID, Name: name, Color: color}
	require.NoError(t, db.Insert(t.Context(), label))
	require.NoError(t, db.Insert(t.Context(), &issues_model.IssueLabel{IssueID: issueID, LabelID: label.ID}))
	return label.ID
}

// TestMigrateIssueTypesSeedsSixInstanceTypesOnceAndConvertsLabels covers every case the
// migration must get right: the seed runs once and stays put, an issue with no repo or org
// type falls back to the instance one, an unknown name creates a repo-scoped type carrying the
// label's own colour, an org-scoped type shadows the instance one for a repo under that
// organization, the lower label id wins on an issue carrying two, an issue that already has an
// assignment is left alone, and a full rerun inserts nothing at all.
//
// unittest.PrepareTestDatabase is not called here: TestMigrateRenamesTheOldTables already
// loaded the fixtures this test reads, and reloading would trip over its narrowed schema.
func TestMigrateIssueTypesSeedsSixInstanceTypesOnceAndConvertsLabels(t *testing.T) {
	ctx := t.Context()
	e := db.GetEngine(ctx)

	// issue 1 belongs to repo 1 (user2, a personal repository: no organization to shadow
	// through), so a plain type:bug label falls all the way back to the instance type.
	insertTypeLabel(t, 1, 1, "type:bug", "#d73a4a")

	// issue 2, same repository, carries a name no type declares anywhere yet.
	insertTypeLabel(t, 1, 2, "type:widget", "#00ff00")

	// issue 3 carries two type labels; the label inserted first gets the lower id and must
	// be the one that wins, regardless of insertion order into issue_label or alphabetical
	// order of the names themselves.
	insertTypeLabel(t, 1, 3, "type:feature", "#0000ff")
	insertTypeLabel(t, 1, 3, "type:bug", "#d73a4a")

	// issue 5 already has a recorded type; the migration must not touch it even though it
	// also carries a type: label.
	require.NoError(t, planning_model.UpsertAssignment(ctx, 5, 999))
	insertTypeLabel(t, 1, 5, "type:task", "#57606a")

	// repo 3 belongs to org 3: an org-scoped "bug" type must shadow the instance one for
	// issue 6, which belongs to that repository.
	orgBug := &planning_model.IssueType{OrgID: 3, Name: "bug", Color: "#123456", Icon: "octicon-flame", Rank: 3}
	require.NoError(t, planning_model.InsertIssueType(ctx, orgBug))
	insertTypeLabel(t, 3, 6, "type:bug", "#d73a4a")

	// issue 11's label spells both the prefix and the value in mixed case; lower-casing must
	// happen before TrimPrefix, or this creates a type literally named "type:bug".
	insertTypeLabel(t, 1, 11, "Type:Bug", "#d73a4a")

	require.NoError(t, migrateIssueTypes(ctx, e))

	instanceTypes, err := planning_model.TypesInScopes(ctx, 0, 0)
	require.NoError(t, err)
	require.Len(t, instanceTypes, 6, "the six seeded types, once")
	byName := map[string]*planning_model.IssueType{}
	for _, it := range instanceTypes {
		byName[it.Name] = it
	}
	for name, rank := range map[string]int{"epic": 1, "feature": 2, "story": 3, "bug": 3, "spike": 3, "task": 4} {
		require.Contains(t, byName, name)
		assert.Equal(t, rank, byName[name].Rank, name)
	}

	assigned, err := planning_model.AssignmentsFor(ctx, []int64{1, 2, 3, 5, 6, 11})
	require.NoError(t, err)

	assert.Equal(t, byName["bug"].ID, assigned[1], "issue 1 falls back to the instance bug type")

	widgetType, err := planning_model.GetIssueType(ctx, assigned[2])
	require.NoError(t, err)
	assert.Equal(t, "widget", widgetType.Name)
	assert.EqualValues(t, 1, widgetType.RepoID, "created scoped to the issue's own repository")
	assert.Equal(t, "#00ff00", widgetType.Color, "carries the label's own colour")
	assert.Equal(t, "octicon-issue-opened", widgetType.Icon)
	assert.Equal(t, 3, widgetType.Rank)

	winningType, err := planning_model.GetIssueType(ctx, assigned[3])
	require.NoError(t, err)
	assert.Equal(t, "feature", winningType.Name, "the label with the lower id wins")

	assert.EqualValues(t, 999, assigned[5], "an issue that already has a type is left alone")

	assert.Equal(t, orgBug.ID, assigned[6], "the org's own bug type shadows the instance one")

	assert.Equal(t, byName["bug"].ID, assigned[11], "a mixed-case type: label still resolves to the lower-cased type")

	typeCountBefore, err := e.Count(new(planning_model.IssueType))
	require.NoError(t, err)
	assignmentCountBefore, err := e.Count(new(planning_model.IssueTypeAssignment))
	require.NoError(t, err)

	require.NoError(t, migrateIssueTypes(ctx, e))

	typeCountAfter, err := e.Count(new(planning_model.IssueType))
	require.NoError(t, err)
	assignmentCountAfter, err := e.Count(new(planning_model.IssueTypeAssignment))
	require.NoError(t, err)
	assert.Equal(t, typeCountBefore, typeCountAfter, "a rerun creates no new type")
	assert.Equal(t, assignmentCountBefore, assignmentCountAfter, "a rerun inserts no new assignment")
}
