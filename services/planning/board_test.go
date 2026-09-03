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

func card(number, columnID int64, labels, assignees []string) Card {
	return Card{
		IssueID: 9000 + number, Number: number, Title: "issue", ColumnID: columnID,
		Labels: labels, Assignees: assignees,
	}
}

// laneKeys renders the lanes as key/label/empty triples, which is what the assertions in
// this file compare: the shape of the board, not its cards.
func laneKeys(lanes []Lane) [][3]any {
	out := make([][3]any, 0, len(lanes))
	for _, lane := range lanes {
		out = append(out, [3]any{lane.Key, lane.Label, lane.IsEmptyValue})
	}
	return out
}

func TestDeliveryParseGroupingAcceptsTheDeclaredSetAndRefusesTheRest(t *testing.T) {
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
}

// By type, by assignee and by epic.
func TestDeliveryBuildLanesGroupsByEveryDeclaredDimension(t *testing.T) {
	cards := []Card{
		card(1, 11, []string{"type:bug", "epic:checkout"}, []string{"alice"}),
		card(2, 12, []string{"type:task", "epic:checkout"}, []string{"bob"}),
		card(3, 13, []string{"type:bug", "epic:billing"}, []string{"alice"}),
	}

	byType := BuildLanes(boardColumns, cards, GroupType)
	assert.Equal(t, [][3]any{{"bug", "bug", false}, {"task", "task", false}}, laneKeys(byType))

	byAssignee := BuildLanes(boardColumns, cards, GroupAssignee)
	assert.Equal(t, [][3]any{{"alice", "alice", false}, {"bob", "bob", false}}, laneKeys(byAssignee))

	byEpic := BuildLanes(boardColumns, cards, GroupEpic)
	assert.Equal(t, [][3]any{{"billing", "billing", false}, {"checkout", "checkout", false}}, laneKeys(byEpic))

	none := BuildLanes(boardColumns, cards, GroupNone)
	require.Len(t, none, 1, "grouping off is Gitea's own board: one lane")
	assert.Equal(t, singleLaneLabel, none[0].Label)
	assert.Equal(t, 3, none[0].Cards)
}

func TestDeliveryBuildLanesCarriesEveryColumnInOrderInEveryLane(t *testing.T) {
	cards := []Card{card(1, 11, []string{"type:bug"}, nil), card(2, 13, []string{"type:task"}, nil)}
	lanes := BuildLanes(boardColumns, cards, GroupType)
	require.Len(t, lanes, 2)
	for _, lane := range lanes {
		require.Len(t, lane.Columns, 3, "a lane carries every column so the board is a rectangle")
		assert.Equal(t, []int64{11, 12, 13}, []int64{lane.Columns[0].ColumnID, lane.Columns[1].ColumnID, lane.Columns[2].ColumnID})
		assert.Equal(t, []string{"Todo", "In Progress", "Done"},
			[]string{lane.Columns[0].Title, lane.Columns[1].Title, lane.Columns[2].Title})
	}
	assert.Len(t, lanes[0].Columns[0].Cards, 1, "the bug lands in Todo")
	assert.Empty(t, lanes[0].Columns[2].Cards)
	assert.Len(t, lanes[1].Columns[2].Cards, 1, "the task lands in Done")
}

// Nothing disappears from a board because a field is unset.
func TestDeliveryBuildLanesPutsAnUnsetValueInAnExplicitEmptyLane(t *testing.T) {
	cards := []Card{
		card(1, 11, []string{"type:bug"}, []string{"alice"}),
		card(2, 11, nil, nil),
	}

	for _, tc := range []struct {
		grouping Grouping
		label    string
	}{
		{GroupType, "no type label"},
		{GroupAssignee, "unassigned"},
		{GroupEpic, "no epic label"},
	} {
		t.Run(string(tc.grouping), func(t *testing.T) {
			lanes := BuildLanes(boardColumns, cards, tc.grouping)
			require.NotEmpty(t, lanes)
			last := lanes[len(lanes)-1]
			assert.True(t, last.IsEmptyValue, "the empty-value lane sorts last")
			assert.Equal(t, tc.label, last.Label, "the lane says WHY it is empty, not just that it is")
			assert.Equal(t, "", last.Key)

			total := 0
			for _, lane := range lanes {
				total += lane.Cards
			}
			assert.Equal(t, len(cards), total, "every card is on the board under every grouping")
		})
	}
}

func TestDeliveryBuildLanesIsStableForACardCarryingTwoValues(t *testing.T) {
	cards := []Card{card(1, 11, []string{"type:task", "type:bug"}, []string{"zoe", "alice"})}

	byType := BuildLanes(boardColumns, cards, GroupType)
	require.Len(t, byType, 1)
	assert.Equal(t, "bug", byType[0].Key, "the lexicographically first value wins, so two renders agree")

	byAssignee := BuildLanes(boardColumns, cards, GroupAssignee)
	require.Len(t, byAssignee, 1)
	assert.Equal(t, "alice", byAssignee[0].Key)
}

func TestDeliveryBuildLanesOnAnEmptyBoardStillRendersItsColumns(t *testing.T) {
	lanes := BuildLanes(boardColumns, nil, GroupType)
	require.Len(t, lanes, 1)
	assert.True(t, lanes[0].IsEmptyValue)
	assert.Len(t, lanes[0].Columns, 3, "an empty board is still a board with columns")
	assert.Equal(t, 0, lanes[0].Cards)
}

// The two writes and no others. A lane move edits the grouping field itself.
func TestDeliveryPlanLaneMoveEditsTheGroupingFieldItself(t *testing.T) {
	write, err := PlanLaneMove(GroupType, "bug")
	require.NoError(t, err)
	assert.Equal(t, LaneWrite{Kind: LaneWriteLabel, Prefix: TypeLabelPrefix, Label: "type:bug"}, write)

	write, err = PlanLaneMove(GroupEpic, "checkout")
	require.NoError(t, err)
	assert.Equal(t, LaneWrite{Kind: LaneWriteLabel, Prefix: EpicLabelPrefix, Label: "epic:checkout"}, write)

	write, err = PlanLaneMove(GroupAssignee, "alice")
	require.NoError(t, err)
	assert.Equal(t, LaneWrite{Kind: LaneWriteAssignee, Assignee: "alice"}, write)
}

// Moving INTO the empty-value lane clears the field rather than being refused: the lane is
// explicit, so it has to be reachable.
func TestDeliveryPlanLaneMoveIntoTheEmptyLaneClearsTheField(t *testing.T) {
	write, err := PlanLaneMove(GroupType, "")
	require.NoError(t, err)
	assert.Equal(t, TypeLabelPrefix, write.Prefix)
	assert.Empty(t, write.Label, "the namespace is cleared and nothing is added")

	write, err = PlanLaneMove(GroupAssignee, "  ")
	require.NoError(t, err)
	assert.Equal(t, LaneWriteAssignee, write.Kind)
	assert.Empty(t, write.Assignee)
}

// A lane move is refused when grouping is off, because there is nothing to write.
func TestDeliveryPlanLaneMoveIsRefusedWhenGroupingIsOff(t *testing.T) {
	_, err := PlanLaneMove(GroupNone, "bug")
	require.Error(t, err)

	var hubErr *hub_model.Error
	require.ErrorAs(t, err, &hubErr)
	assert.Contains(t, hubErr.Message, "not grouped")
	assert.NotEmpty(t, hubErr.SuggestedAction, "every error carries a suggested next action")
	assert.Contains(t, hubErr.SuggestedAction, "COLUMNS", "the refusal names the write that does still work")
}
