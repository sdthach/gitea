// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package planning holds the board and roadmap view logic: group grouping, the roadmap
// bar model, and the ruler that ticks it.
package planning

import (
	"sort"
	"strconv"
	"strings"

	hub_model "gitea.dev/models/hub"
)

// The board renders Gitea's project columns vertically and adds horizontal groups, a
// dimension Gitea does not model: project.Column carries ID, Title, Default, Sorting,
// Color, ProjectID, CreatorID and timestamps and no group or row field (verified in
// models/project/column.go). Groups are therefore a RENDERING over rows the Projects API
// already returns — no schema change, no migration and no fork change.
//
// Everything in this file is pure: it takes reduced structs and returns reduced structs, so
// group assignment is tested with no database and no server.

// Grouping is the group dimension, chosen at view time. It is never stored on the project,
// so two people may group the same board differently.
type Grouping string

const (
	// GroupNone renders the board with a single group, which is Gitea's own board.
	GroupNone Grouping = "none"
	// GroupType groups by the issue's assigned type — its plan_issue_type_assignment row,
	// not a label.
	GroupType Grouping = "type"
	// GroupAssignee groups by the issue's assignee.
	GroupAssignee Grouping = "assignee"
	// GroupParent groups by root work item — plan_issue_parent — replacing the retired
	// label convention. A card lands under its root ancestor's own row; a root with no
	// children of its own has nothing to hold a row open and lands in the empty group.
	GroupParent Grouping = "parent"
)

// Groupings is the accepted set, in declaration order. The API rejects anything else
// naming this list, so an unknown grouping is refused rather than silently treated as none.
var Groupings = []string{string(GroupType), string(GroupAssignee), string(GroupParent), string(GroupNone)}

// Group keys and labels for the explicit empty-value group. An issue with no value for
// the active grouping lands here; nothing disappears from a board because a field is unset.
const emptyGroupKey = ""

// singleGroupLabel names the one group grouping "none" renders.
const singleGroupLabel = "All issues"

// ParseGrouping resolves a caller's word. An empty string means none, so a board with no
// grouping parameter renders Gitea's own board rather than being refused.
func ParseGrouping(s string) (Grouping, bool) {
	switch Grouping(strings.TrimSpace(strings.ToLower(s))) {
	case "", GroupNone:
		return GroupNone, true
	case GroupType:
		return GroupType, true
	case GroupAssignee:
		return GroupAssignee, true
	case GroupParent:
		return GroupParent, true
	}
	return GroupNone, false
}

// Card is one issue on the board, reduced to what group assignment depends on.
type Card struct {
	IssueID  int64  `json:"issue_id"`
	Number   int64  `json:"number"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	ColumnID int64  `json:"column_id"`
	Sorting  int64  `json:"sorting"`
	// Type is the assigned type's name, empty when none; TypeID, TypeColor and TypeIcon
	// come from the same assignment. Assignees is the other grouping field. A group move
	// edits whichever field the active grouping names.
	Type      string   `json:"type,omitempty"`
	TypeID    int64    `json:"type_id,omitempty"`
	TypeColor string   `json:"type_color,omitempty"`
	TypeIcon  string   `json:"type_icon,omitempty"`
	Labels    []string `json:"labels"`
	Assignees []string `json:"assignees"`
	IsClosed  bool     `json:"is_closed"`
	IsPull    bool     `json:"is_pull"`
	// ParentIssueID is the issue's own recorded parent, 0 when it has none. RootIssueID is
	// the top of its chain — itself when ParentIssueID is 0. Depth is its distance from that
	// root. HasChildren says whether this issue itself has any recorded child.
	ParentIssueID int64 `json:"parent_issue_id,omitempty"`
	RootIssueID   int64 `json:"root_issue_id,omitempty"`
	Depth         int   `json:"depth,omitempty"`
	HasChildren   bool  `json:"has_children,omitempty"`
}

// TreeEdge is one parent edge, published beside the cards or bars so a client can draw the
// hierarchy without re-deriving it from every card's own parent_issue_id.
type TreeEdge struct {
	IssueID       int64 `json:"issue_id"`
	ParentIssueID int64 `json:"parent_issue_id"`
}

// BuildTree renders a repo's parent map as the published edge list, sorted by child issue id
// so the result is stable across renders of the same data.
func BuildTree(parents map[int64]int64) []TreeEdge {
	out := make([]TreeEdge, 0, len(parents))
	for child, parent := range parents {
		out = append(out, TreeEdge{IssueID: child, ParentIssueID: parent})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IssueID < out[j].IssueID })
	return out
}

// BoardColumn is one of Gitea's project columns, in the order the Projects API returns it.
type BoardColumn struct {
	ID      int64  `json:"column_id"`
	Title   string `json:"title"`
	Color   string `json:"color,omitempty"`
	Default bool   `json:"default"`
}

// GroupColumn is one column within one group: the cells of the board's grid.
type GroupColumn struct {
	ColumnID int64  `json:"column_id"`
	Title    string `json:"title"`
	Cards    []Card `json:"cards"`
}

// Group is one horizontal row of the board.
type Group struct {
	// Key is the grouping value itself — the text a group move writes, or the root issue's
	// id (as a string) under parent grouping. Empty is the empty-value group.
	Key   string `json:"key"`
	Label string `json:"label"`
	// IsEmptyValue marks the issues with no value for the active
	// grouping, shown rather than dropped.
	IsEmptyValue bool          `json:"is_empty_value"`
	Columns      []GroupColumn `json:"columns"`
	Cards        int           `json:"cards"`
	// RootIssueID is the row's own root work item, 0 outside parent grouping.
	RootIssueID int64 `json:"root_issue_id,omitempty"`
}

// emptyGroupLabel names the empty-value group per grouping, so it reads as a fact about the
// issues rather than as a blank row.
func emptyGroupLabel(g Grouping) string {
	switch g {
	case GroupType:
		return "no type assigned"
	case GroupAssignee:
		return "unassigned"
	case GroupParent:
		return "no parent"
	}
	return singleGroupLabel
}

// GroupInput is one row reduced to what group assignment depends on: the issue's assigned
// type name, its assignees, and — for parent grouping — its root ancestor, already resolved
// by the caller. It takes a struct rather than positional slices so the chart's bars and the
// board's cards reach one definition of a group without either side guessing which field is
// which.
type GroupInput struct {
	TypeName    string
	Assignees   []string
	RootIssueID int64
	// HasChildren is whether the ROOT WORK ITEM (RootIssueID) itself has any recorded child —
	// not whether this particular card does. A root with no children of its own has nothing
	// to hold a row open under parent grouping.
	HasChildren bool
}

// GroupKeyFor is the whole of group assignment. A row with no value for the active grouping
// returns the empty key, which BuildGroups renders as the explicit empty-value group.
//
// A row with more than one candidate value under assignee grouping lands in the
// lexicographically first, so two renders of the same data never disagree.
func GroupKeyFor(in GroupInput, grouping Grouping) string {
	switch grouping {
	case GroupType:
		return strings.TrimSpace(in.TypeName)
	case GroupParent:
		if !in.HasChildren {
			return emptyGroupKey
		}
		return strconv.FormatInt(in.RootIssueID, 10)
	case GroupAssignee:
		if len(in.Assignees) == 0 {
			return emptyGroupKey
		}
		sorted := append([]string(nil), in.Assignees...)
		sort.Strings(sorted)
		return sorted[0]
	}
	return emptyGroupKey
}

// groupInputFor reduces one card to the GroupInput every grouping reads, resolving the
// parent-grouping fields from the card's own hierarchy fields: a card that is not its own
// root proves its root has at least one child (itself), with no second lookup needed.
func groupInputFor(card Card) GroupInput {
	return GroupInput{
		TypeName: card.Type, Assignees: card.Assignees,
		RootIssueID: card.RootIssueID,
		HasChildren: card.HasChildren || (card.RootIssueID != 0 && card.RootIssueID != card.IssueID),
	}
}

// BuildGroups renders the board: every group carries every column, in the Projects API's own
// order, so the result is a rectangle a template can lay out without knowing the grouping.
//
// The empty-value group is always last and is emitted only when it holds a card — except
// under GroupNone, where the single group IS the board and is always emitted.
func BuildGroups(columns []BoardColumn, cards []Card, grouping Grouping) []Group {
	titleByIssue := make(map[int64]string, len(cards))
	for _, card := range cards {
		titleByIssue[card.IssueID] = card.Title
	}

	byGroup := map[string][]Card{}
	order := make([]string, 0, 8)
	labelForKey := map[string]string{}
	rootForKey := map[string]int64{}
	for _, card := range cards {
		key := GroupKeyFor(groupInputFor(card), grouping)
		if _, seen := byGroup[key]; !seen {
			order = append(order, key)
		}
		byGroup[key] = append(byGroup[key], card)
		if grouping == GroupParent && key != emptyGroupKey {
			if _, ok := labelForKey[key]; !ok {
				labelForKey[key] = titleByIssue[card.RootIssueID]
				rootForKey[key] = card.RootIssueID
			}
		}
	}
	if grouping == GroupNone {
		byGroup = map[string][]Card{emptyGroupKey: cards}
		order = []string{emptyGroupKey}
	}
	if len(order) == 0 {
		order = []string{emptyGroupKey}
		byGroup[emptyGroupKey] = nil
	}

	sort.Slice(order, func(i, j int) bool {
		// The empty-value group is explicit, and it sorts last so the named groups
		// read first.
		if (order[i] == emptyGroupKey) != (order[j] == emptyGroupKey) {
			return order[j] == emptyGroupKey
		}
		return order[i] < order[j]
	})

	groups := make([]Group, 0, len(order))
	for _, key := range order {
		group := Group{Key: key, Label: key, IsEmptyValue: key == emptyGroupKey}
		switch {
		case group.IsEmptyValue:
			group.Label = emptyGroupLabel(grouping)
		case grouping == GroupParent:
			group.Label = labelForKey[key]
			group.RootIssueID = rootForKey[key]
		}
		group.Columns = make([]GroupColumn, 0, len(columns))
		for _, col := range columns {
			lc := GroupColumn{ColumnID: col.ID, Title: col.Title, Cards: []Card{}}
			for _, card := range byGroup[key] {
				if card.ColumnID == col.ID {
					lc.Cards = append(lc.Cards, card)
				}
			}
			sort.SliceStable(lc.Cards, func(i, j int) bool {
				if lc.Cards[i].Sorting != lc.Cards[j].Sorting {
					return lc.Cards[i].Sorting < lc.Cards[j].Sorting
				}
				return lc.Cards[i].Number < lc.Cards[j].Number
			})
			group.Cards += len(lc.Cards)
			group.Columns = append(group.Columns, lc)
		}
		groups = append(groups, group)
	}
	return groups
}

// GroupsMissingRootTitle lists the RootIssueID of every parent-grouped row BuildGroups left
// unlabelled: its root was not among the cards it saw, so a caller with database access must
// fetch the title itself.
func GroupsMissingRootTitle(groups []Group) []int64 {
	ids := make([]int64, 0, 2)
	for _, g := range groups {
		if g.RootIssueID != 0 && g.Label == "" {
			ids = append(ids, g.RootIssueID)
		}
	}
	return ids
}

// ApplyRootTitles fills in the label of every parent-grouped row GroupsMissingRootTitle named,
// from a caller's own fetch of those roots.
func ApplyRootTitles(groups []Group, titles map[int64]string) {
	for i := range groups {
		if groups[i].RootIssueID != 0 && groups[i].Label == "" {
			groups[i].Label = titles[groups[i].RootIssueID]
		}
	}
}

// roadmapGroupColumn is the one column the chart's groups carry. A chart has no columns of its
// own, so BuildGroups lays every bar of a group into a single cell and the result is still the
// rectangle a template can walk.
const roadmapGroupColumn = "bars"

// RoadmapGroups groups the chart's bars through the board's own group definition, so a group on
// the chart and a group on the board are the same value read the same way.
func RoadmapGroups(bars []Bar, grouping Grouping) []Group {
	cards := make([]Card, 0, len(bars))
	for _, bar := range bars {
		cards = append(cards, Card{
			IssueID: bar.IssueID, Number: bar.Number, Title: bar.Title, URL: bar.URL,
			Type: bar.Type, TypeID: bar.TypeID, TypeColor: bar.TypeColor, TypeIcon: bar.TypeIcon,
			Labels: bar.Labels, Assignees: bar.Assignees, IsClosed: bar.IsClosed,
			ParentIssueID: bar.ParentIssueID, RootIssueID: bar.RootIssueID,
			Depth: bar.Depth, HasChildren: bar.HasChildren,
		})
	}
	return BuildGroups([]BoardColumn{{Title: roadmapGroupColumn}}, cards, grouping)
}

// GroupWriteKind is what a group move edits.
type GroupWriteKind string

const (
	// GroupWriteAssignee replaces the card's assignee.
	GroupWriteAssignee GroupWriteKind = "assignee"
	// GroupWriteType assigns the card the type named TypeName, resolved to a visible type
	// by the handler — PlanGroupMove itself knows nothing of what types exist.
	GroupWriteType GroupWriteKind = "type"
	// GroupWriteParent sets or removes the card's recorded parent — resolved and enforced by
	// the handler through SetIssueParent, which is where every hierarchy refusal lives.
	GroupWriteParent GroupWriteKind = "parent"
)

// GroupWrite is the single edit a group move performs. It names the field itself — the
// grouping value is not stored anywhere else, so moving between groups IS editing it.
type GroupWrite struct {
	Kind GroupWriteKind
	// Assignee is the login to assign; empty means clear the assignees.
	Assignee string
	// TypeName is the target type's name for a GroupWriteType move, lower-cased; empty
	// clears the issue's type.
	TypeName string
	// ParentIssueID is the target root's issue id for a GroupWriteParent move; 0 means the
	// card is moving into the empty group, so its parent is removed rather than set.
	ParentIssueID int64
}

// PlanGroupMove resolves a group move into the one field edit it performs, or refuses it.
//
// A group move is REFUSED when grouping is off, because there is nothing to write. The
// refusal carries what to do about it.
func PlanGroupMove(grouping Grouping, groupKey string) (GroupWrite, error) {
	switch grouping {
	case GroupType:
		return GroupWrite{Kind: GroupWriteType, TypeName: strings.ToLower(strings.TrimSpace(groupKey))}, nil
	case GroupParent:
		key := strings.TrimSpace(groupKey)
		if key == "" {
			return GroupWrite{Kind: GroupWriteParent}, nil
		}
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			return GroupWrite{}, &hub_model.Error{
				Message:         "the parent group's key must be a root issue id",
				SuggestedAction: "Move to one of the group keys the board or roadmap itself publishes.",
			}
		}
		return GroupWrite{Kind: GroupWriteParent, ParentIssueID: id}, nil
	case GroupAssignee:
		return GroupWrite{Kind: GroupWriteAssignee, Assignee: strings.TrimSpace(groupKey)}, nil
	}
	return GroupWrite{}, &hub_model.Error{
		Message: "the board is not grouped, so a group move has no field to write",
		SuggestedAction: "Group the board by type, assignee or parent first — a group is the grouping value, and with grouping off there is only one group. " +
			"Moving a card between COLUMNS works with grouping off.",
	}
}
