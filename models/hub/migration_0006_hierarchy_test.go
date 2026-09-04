// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"testing"

	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"
	"gitea.dev/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hierarchyIssue inserts one bare issue directly, mirroring the shape the roadmap's own
// paging tests already insert issues with.
func hierarchyIssue(t *testing.T, id, repoID, index int64) {
	t.Helper()
	now := timeutil.TimeStampNow()
	require.NoError(t, db.Insert(t.Context(), &issues_model.Issue{
		ID: id, RepoID: repoID, Index: index, PosterID: 2, Title: "hierarchy migration fixture",
		CreatedUnix: now, UpdatedUnix: now,
	}))
}

// hierarchyLabel creates one repo-scoped label, returning its id so several issues can share it
// the way every issue under one epic shares its epic: label.
func hierarchyLabel(t *testing.T, repoID int64, name string) int64 {
	t.Helper()
	label := &issues_model.Label{RepoID: repoID, Name: name, Color: "#8250df"}
	require.NoError(t, db.Insert(t.Context(), label))
	return label.ID
}

func hierarchyAttachLabel(t *testing.T, issueID, labelID int64) {
	t.Helper()
	require.NoError(t, db.Insert(t.Context(), &issues_model.IssueLabel{IssueID: issueID, LabelID: labelID}))
}

func hierarchyAssignType(t *testing.T, issueID, typeID int64) {
	t.Helper()
	require.NoError(t, planning_model.UpsertAssignment(t.Context(), issueID, typeID))
}

// hierarchyInstanceType reads one of migration 5's seeded instance types by name; migration 5
// runs before this file's tests in file-name order and leaves them in place.
func hierarchyInstanceType(t *testing.T, e db.Engine, name string) *planning_model.IssueType {
	t.Helper()
	types, err := planning_model.TypesInScopes(t.Context(), 0, 0)
	require.NoError(t, err)
	for _, ty := range types {
		if ty.Name == name {
			return ty
		}
	}
	t.Fatalf("instance type %q was not seeded", name)
	return nil
}

// TestMigrateHierarchyConvertsBothEpicLabelConventionsAndSkipsTheRest covers the numeric
// convention, the named-epic-issue convention, a self-referencing label, an ambiguous named
// epic, a rank-refused pair (equal rank, and an untyped side), and a rerun that inserts nothing.
//
// unittest.PrepareTestDatabase is not called here: TestMigrateRenamesTheOldTables already
// loaded the fixtures this test reads, and reloading would trip over its narrowed schema and
// migration 5's own seeded types.
func TestMigrateHierarchyConvertsBothEpicLabelConventionsAndSkipsTheRest(t *testing.T) {
	ctx := t.Context()
	e := db.GetEngine(ctx)

	epicType := hierarchyInstanceType(t, e, "epic")   // rank 1
	storyType := hierarchyInstanceType(t, e, "story") // rank 3
	taskType := hierarchyInstanceType(t, e, "task")   // rank 4

	// Numeric convention: issue 4 already exists (repo 2, index 1) from the fixtures and
	// carries no type yet; give it one so the rank check has something to compare against.
	hierarchyAssignType(t, 4, storyType.ID)
	hierarchyIssue(t, 9601, 2, 9601)
	hierarchyAttachLabel(t, 9601, hierarchyLabel(t, 2, "epic:1"))
	hierarchyAssignType(t, 9601, taskType.ID)

	// Named convention: the epic issue names itself with the very label its children carry.
	hierarchyIssue(t, 9602, 2, 9602)
	hierarchyAssignType(t, 9602, epicType.ID)
	hierarchyAttachLabel(t, 9602, hierarchyLabel(t, 2, "epic:widgets"))
	hierarchyIssue(t, 9603, 2, 9603)
	hierarchyAttachLabel(t, 9603, hierarchyLabel(t, 2, "epic:widgets"))
	hierarchyAssignType(t, 9603, storyType.ID)

	// Self: an issue's own index is the value its epic: label names.
	hierarchyIssue(t, 9604, 2, 102)
	hierarchyAttachLabel(t, 9604, hierarchyLabel(t, 2, "epic:102"))

	// Ambiguous: two candidate epics both typed epic and carrying the same self-naming label.
	hierarchyIssue(t, 9605, 2, 9605)
	hierarchyAssignType(t, 9605, epicType.ID)
	ambigLabel := hierarchyLabel(t, 2, "epic:ambig")
	hierarchyAttachLabel(t, 9605, ambigLabel)
	hierarchyIssue(t, 9606, 2, 9606)
	hierarchyAssignType(t, 9606, epicType.ID)
	hierarchyAttachLabel(t, 9606, ambigLabel)
	hierarchyIssue(t, 9607, 2, 9607)
	hierarchyAttachLabel(t, 9607, ambigLabel)
	hierarchyAssignType(t, 9607, taskType.ID)

	// Rank-refused, equal rank on both sides.
	hierarchyIssue(t, 9608, 2, 9608)
	hierarchyAssignType(t, 9608, storyType.ID)
	hierarchyIssue(t, 9609, 2, 9609)
	hierarchyAttachLabel(t, 9609, hierarchyLabel(t, 2, "epic:9608"))
	hierarchyAssignType(t, 9609, storyType.ID)

	// Rank-refused, the child carries no type at all.
	hierarchyIssue(t, 9610, 2, 9610)
	hierarchyAssignType(t, 9610, storyType.ID)
	hierarchyIssue(t, 9611, 2, 9611)
	hierarchyAttachLabel(t, 9611, hierarchyLabel(t, 2, "epic:9610"))

	// A pull request carrying an epic: label is skipped: hierarchy links issues only.
	now := timeutil.TimeStampNow()
	require.NoError(t, db.Insert(ctx, &issues_model.Issue{
		ID: 9612, RepoID: 2, Index: 9612, PosterID: 2, Title: "hierarchy migration fixture", IsPull: true,
		CreatedUnix: now, UpdatedUnix: now,
	}))
	hierarchyAttachLabel(t, 9612, hierarchyLabel(t, 2, "epic:9601"))

	// A shorter value must not resolve to a longer label that merely contains it as a
	// substring — the match against the epic issue's own label is exact.
	hierarchyIssue(t, 9613, 2, 9613)
	hierarchyAssignType(t, 9613, epicType.ID)
	hierarchyAttachLabel(t, 9613, hierarchyLabel(t, 2, "epic:checkout"))
	hierarchyIssue(t, 9614, 2, 9614)
	hierarchyAttachLabel(t, 9614, hierarchyLabel(t, 2, "epic:checkout2"))
	hierarchyAssignType(t, 9614, storyType.ID)

	require.NoError(t, migrateHierarchy(ctx, e))

	assertParent := func(childID, wantParent int64, msg string) {
		row := new(planning_model.IssueParent)
		has, err := e.Where("child_issue_id = ?", childID).Get(row)
		require.NoError(t, err)
		require.True(t, has, msg)
		assert.Equal(t, wantParent, row.ParentIssueID, msg)
	}
	assertNoParent := func(childID int64, msg string) {
		has, err := e.Where("child_issue_id = ?", childID).Exist(new(planning_model.IssueParent))
		require.NoError(t, err)
		assert.False(t, has, msg)
	}

	assertParent(9601, 4, "numeric convention: epic:1 resolves to the issue indexed 1 in repo 2")
	assertParent(9603, 9602, "named convention: the epic issue names itself with epic:widgets")
	assertNoParent(9604, "self-referencing label is skipped")
	assertNoParent(9607, "two candidate epics make the named lookup ambiguous")
	assertNoParent(9609, "equal rank on both sides refuses the link")
	assertNoParent(9611, "an untyped child refuses the link")
	assertNoParent(9612, "a pull request carrying an epic: label is skipped")
	assertNoParent(9614, "a shorter value must not resolve as a substring of a longer label")

	countBefore, err := e.Count(new(planning_model.IssueParent))
	require.NoError(t, err)

	require.NoError(t, migrateHierarchy(ctx, e))

	countAfter, err := e.Count(new(planning_model.IssueParent))
	require.NoError(t, err)
	assert.Equal(t, countBefore, countAfter, "a rerun inserts no new parent row")
}
