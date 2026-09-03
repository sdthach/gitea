// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"testing"

	hub_model "gitea.dev/models/hub"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var boardColumns = []BoardColumn{
	{ID: 11, Title: "Todo", Default: true},
	{ID: 12, Title: "In Progress"},
	{ID: 13, Title: "Done"},
}

// card builds a test card. typeName is the assigned type's name (empty for none).
func card(number, columnID int64, typeName string, labels, assignees []string) Card {
	return Card{
		IssueID: 9000 + number, Number: number, Title: "issue", ColumnID: columnID,
		Type: typeName, Labels: labels, Assignees: assignees,
	}
}

// parentCard builds a card carrying hierarchy fields, for GroupParent's own tests.
func parentCard(issueID, columnID, parentID, rootID int64, hasChildren bool, title string) Card {
	return Card{
		IssueID: issueID, Number: issueID, Title: title, ColumnID: columnID,
		ParentIssueID: parentID, RootIssueID: rootID, HasChildren: hasChildren,
	}
}

// groupKeys renders the groups as key/label/empty triples, which is what the assertions in
// this file compare: the shape of the board, not its cards.
func groupKeys(groups []Group) [][3]any {
	out := make([][3]any, 0, len(groups))
	for _, group := range groups {
		out = append(out, [3]any{group.Key, group.Label, group.IsEmptyValue})
	}
	return out
}

func TestPlanningParseGroupingAcceptsTheDeclaredSetAndRefusesTheRest(t *testing.T) {
	for _, name := range Groupings {
		got, ok := ParseGrouping(name)
		assert.True(t, ok, "%q is a declared grouping", name)
		assert.Equal(t, Grouping(name), got)
	}

	got, ok := ParseGrouping("")
	assert.True(t, ok, "no grouping parameter renders Gitea's own board rather than being refused")
	assert.Equal(t, GroupNone, got)

	got, ok = ParseGrouping("  ASSIGNEE ")
	assert.True(t, ok)
	assert.Equal(t, GroupAssignee, got)

	_, ok = ParseGrouping("milestone")
	assert.False(t, ok, "an unknown grouping is refused, never silently treated as none")

	_, ok = ParseGrouping("epic")
	assert.False(t, ok, "the retired epic grouping is refused rather than silently accepted")
}

// By type and by assignee.
func TestPlanningBuildGroupsGroupsByEveryDeclaredDimension(t *testing.T) {
	cards := []Card{
		card(1, 11, "bug", nil, []string{"alice"}),
		card(2, 12, "task", nil, []string{"bob"}),
		card(3, 13, "bug", nil, []string{"alice"}),
	}

	byType := BuildGroups(boardColumns, cards, GroupType)
	assert.Equal(t, [][3]any{{"bug", "bug", false}, {"task", "task", false}}, groupKeys(byType))

	byAssignee := BuildGroups(boardColumns, cards, GroupAssignee)
	assert.Equal(t, [][3]any{{"alice", "alice", false}, {"bob", "bob", false}}, groupKeys(byAssignee))

	none := BuildGroups(boardColumns, cards, GroupNone)
	require.Len(t, none, 1, "grouping off is Gitea's own board: one group")
	assert.Equal(t, singleGroupLabel, none[0].Label)
	assert.Equal(t, 3, none[0].Cards)
}

// A root with children gets its own row, labelled with the root's own title; a childless root
// has nothing to hold a row open and lands in the empty group.
func TestPlanningBuildGroupsByParentRowsUnderTheRootAndEmptiesTheChildless(t *testing.T) {
	cards := []Card{
		parentCard(1, 11, 0, 1, true, "checkout epic"),     // the root itself
		parentCard(2, 12, 1, 1, false, "story one"),        // direct child of the root
		parentCard(3, 13, 2, 1, false, "task under story"), // grandchild, same root
		parentCard(4, 11, 0, 4, false, "standalone"),       // its own root, no children
	}

	groups := BuildGroups(boardColumns, cards, GroupParent)
	require.Len(t, groups, 2)
	assert.Equal(t, "1", groups[0].Key)
	assert.Equal(t, "checkout epic", groups[0].Label, "the row is labelled with the root's own title")
	assert.EqualValues(t, 1, groups[0].RootIssueID)
	assert.Equal(t, 3, groups[0].Cards, "the root plus its whole subtree")

	last := groups[len(groups)-1]
	assert.True(t, last.IsEmptyValue)
	assert.Equal(t, "no parent", last.Label)
	assert.Equal(t, 1, last.Cards, "the childless standalone issue")
}

func TestPlanningBuildGroupsCarriesEveryColumnInOrderInEveryGroup(t *testing.T) {
	cards := []Card{card(1, 11, "bug", nil, nil), card(2, 13, "task", nil, nil)}
	groups := BuildGroups(boardColumns, cards, GroupType)
	require.Len(t, groups, 2)
	for _, group := range groups {
		require.Len(t, group.Columns, 3, "a group carries every column so the board is a rectangle")
		assert.Equal(t, []int64{11, 12, 13}, []int64{group.Columns[0].ColumnID, group.Columns[1].ColumnID, group.Columns[2].ColumnID})
		assert.Equal(t, []string{"Todo", "In Progress", "Done"},
			[]string{group.Columns[0].Title, group.Columns[1].Title, group.Columns[2].Title})
	}
	assert.Len(t, groups[0].Columns[0].Cards, 1, "the bug lands in Todo")
	assert.Empty(t, groups[0].Columns[2].Cards)
	assert.Len(t, groups[1].Columns[2].Cards, 1, "the task lands in Done")
}

// Nothing disappears from a board because a field is unset.
func TestPlanningBuildGroupsPutsAnUnsetValueInAnExplicitEmptyGroup(t *testing.T) {
	cards := []Card{
		card(1, 11, "bug", nil, []string{"alice"}),
		card(2, 11, "", nil, nil),
	}

	for _, tc := range []struct {
		grouping Grouping
		label    string
	}{
		{GroupType, "no type assigned"},
		{GroupAssignee, "unassigned"},
	} {
		t.Run(string(tc.grouping), func(t *testing.T) {
			groups := BuildGroups(boardColumns, cards, tc.grouping)
			require.NotEmpty(t, groups)
			last := groups[len(groups)-1]
			assert.True(t, last.IsEmptyValue, "the empty-value group sorts last")
			assert.Equal(t, tc.label, last.Label, "the group says WHY it is empty, not just that it is")
			assert.Equal(t, "", last.Key)

			total := 0
			for _, group := range groups {
				total += group.Cards
			}
			assert.Equal(t, len(cards), total, "every card is on the board under every grouping")
		})
	}
}

func TestPlanningBuildGroupsIsStableForACardCarryingTwoAssignees(t *testing.T) {
	cards := []Card{card(1, 11, "bug", nil, []string{"zoe", "alice"})}

	byAssignee := BuildGroups(boardColumns, cards, GroupAssignee)
	require.Len(t, byAssignee, 1)
	assert.Equal(t, "alice", byAssignee[0].Key, "the lexicographically first value wins, so two renders agree")
}

func TestPlanningBuildGroupsOnAnEmptyBoardStillRendersItsColumns(t *testing.T) {
	groups := BuildGroups(boardColumns, nil, GroupType)
	require.Len(t, groups, 1)
	assert.True(t, groups[0].IsEmptyValue)
	assert.Len(t, groups[0].Columns, 3, "an empty board is still a board with columns")
	assert.Equal(t, 0, groups[0].Cards)
}

// The three writes and no others. A group move edits the grouping field itself.
func TestPlanningPlanGroupMoveEditsTheGroupingFieldItself(t *testing.T) {
	write, err := PlanGroupMove(GroupType, "Bug")
	require.NoError(t, err)
	assert.Equal(t, GroupWrite{Kind: GroupWriteType, TypeName: "bug"}, write, "the type name is lower-cased, matching storage")

	write, err = PlanGroupMove(GroupParent, "3")
	require.NoError(t, err)
	assert.Equal(t, GroupWrite{Kind: GroupWriteParent, ParentIssueID: 3}, write, "the group key is the root issue id")

	write, err = PlanGroupMove(GroupAssignee, "alice")
	require.NoError(t, err)
	assert.Equal(t, GroupWrite{Kind: GroupWriteAssignee, Assignee: "alice"}, write)
}

// A group key that is not a plausible issue id is refused rather than silently ignored.
func TestPlanningPlanGroupMoveByParentRefusesANonNumericKey(t *testing.T) {
	_, err := PlanGroupMove(GroupParent, "checkout")
	require.Error(t, err)
	var hubErr *hub_model.Error
	require.ErrorAs(t, err, &hubErr)
	assert.NotEmpty(t, hubErr.SuggestedAction)
}

// Moving INTO the empty-value group clears the field rather than being refused: the group is
// explicit, so it has to be reachable.
func TestPlanningPlanGroupMoveIntoTheEmptyGroupClearsTheField(t *testing.T) {
	write, err := PlanGroupMove(GroupType, "")
	require.NoError(t, err)
	assert.Equal(t, GroupWriteType, write.Kind)
	assert.Empty(t, write.TypeName, "an empty type name clears the issue's assigned type")

	write, err = PlanGroupMove(GroupAssignee, "  ")
	require.NoError(t, err)
	assert.Equal(t, GroupWriteAssignee, write.Kind)
	assert.Empty(t, write.Assignee)

	write, err = PlanGroupMove(GroupParent, "")
	require.NoError(t, err)
	assert.Equal(t, GroupWriteParent, write.Kind)
	assert.Zero(t, write.ParentIssueID, "moving to the empty group removes the parent")
}

// A group move is refused when grouping is off, because there is nothing to write.
func TestPlanningPlanGroupMoveIsRefusedWhenGroupingIsOff(t *testing.T) {
	_, err := PlanGroupMove(GroupNone, "bug")
	require.Error(t, err)

	var hubErr *hub_model.Error
	require.ErrorAs(t, err, &hubErr)
	assert.Contains(t, hubErr.Message, "not grouped")
	assert.NotEmpty(t, hubErr.SuggestedAction, "every error carries a suggested next action")
	assert.Contains(t, hubErr.SuggestedAction, "COLUMNS", "the refusal names the write that does still work")
}

func TestBuildTreeIsSortedByChildIssueID(t *testing.T) {
	tree := BuildTree(map[int64]int64{3: 1, 2: 1})
	assert.Equal(t, []TreeEdge{{IssueID: 2, ParentIssueID: 1}, {IssueID: 3, ParentIssueID: 1}}, tree)
}
