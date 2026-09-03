// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package planning holds the board and roadmap view logic: group grouping, the roadmap
// bar model, and the ruler that ticks it.
package planning

import (
	"sort"
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
	// GroupEpic groups by ccpm's epic:<name> label.
	GroupEpic Grouping = "epic"
)

// Groupings is the accepted set, in declaration order. The API rejects anything else
// naming this list, so an unknown grouping is refused rather than silently treated as none.
var Groupings = []string{string(GroupType), string(GroupAssignee), string(GroupEpic), string(GroupNone)}

// EpicLabelPrefix is the label namespace ccpm writes for epic grouping, which hierarchy has
// not yet replaced.
const EpicLabelPrefix = "epic:"

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
	case GroupEpic:
		return GroupEpic, true
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
	// come from the same assignment. Labels and Assignees are the other grouping fields. A
	// group move edits whichever field the active grouping names.
	Type      string   `json:"type,omitempty"`
	TypeID    int64    `json:"type_id,omitempty"`
	TypeColor string   `json:"type_color,omitempty"`
	TypeIcon  string   `json:"type_icon,omitempty"`
	Labels    []string `json:"labels"`
	Assignees []string `json:"assignees"`
	IsClosed  bool     `json:"is_closed"`
	IsPull    bool     `json:"is_pull"`
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
	// Key is the grouping value itself — the text a group move writes. Empty is the
	// empty-value group.
	Key   string `json:"key"`
	Label string `json:"label"`
	// IsEmptyValue marks the issues with no value for the active
	// grouping, shown rather than dropped.
	IsEmptyValue bool          `json:"is_empty_value"`
	Columns      []GroupColumn `json:"columns"`
	Cards        int           `json:"cards"`
}

// emptyGroupLabel names the empty-value group per grouping, so it reads as a fact about the
// issues rather than as a blank row.
func emptyGroupLabel(g Grouping) string {
	switch g {
	case GroupType:
		return "no type assigned"
	case GroupAssignee:
		return "unassigned"
	case GroupEpic:
		return "no epic label"
	}
	return singleGroupLabel
}

// labelValue returns the text after prefix on the first matching label, sorted so a card
// carrying two lands in the same group on every render. Matching is case-insensitive on the
// prefix because Gitea does not case-fold label names.
func labelValue(labels []string, prefix string) string {
	matched := make([]string, 0, 1)
	for _, l := range labels {
		if len(l) > len(prefix) && strings.EqualFold(l[:len(prefix)], prefix) {
			matched = append(matched, l[len(prefix):])
		}
	}
	if len(matched) == 0 {
		return ""
	}
	sort.Strings(matched)
	return matched[0]
}

// GroupInput is one row reduced to what group assignment depends on: the issue's assigned
// type name, its labels (which epic grouping still reads), and its assignees. It takes a
// struct rather than positional slices so the chart's bars and the board's cards reach one
// definition of a group without either side guessing which field is which.
type GroupInput struct {
	TypeName  string
	Labels    []string
	Assignees []string
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
	case GroupEpic:
		return labelValue(in.Labels, EpicLabelPrefix)
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

// BuildGroups renders the board: every group carries every column, in the Projects API's own
// order, so the result is a rectangle a template can lay out without knowing the grouping.
//
// The empty-value group is always last and is emitted only when it holds a card — except
// under GroupNone, where the single group IS the board and is always emitted.
func BuildGroups(columns []BoardColumn, cards []Card, grouping Grouping) []Group {
	byGroup := map[string][]Card{}
	order := make([]string, 0, 8)
	for _, card := range cards {
		key := GroupKeyFor(GroupInput{TypeName: card.Type, Labels: card.Labels, Assignees: card.Assignees}, grouping)
		if _, seen := byGroup[key]; !seen {
			order = append(order, key)
		}
		byGroup[key] = append(byGroup[key], card)
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
		if group.IsEmptyValue {
			group.Label = emptyGroupLabel(grouping)
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
		})
	}
	return BuildGroups([]BoardColumn{{Title: roadmapGroupColumn}}, cards, grouping)
}

// GroupWriteKind is what a group move edits.
type GroupWriteKind string

const (
	// GroupWriteLabel replaces the card's epic: label.
	GroupWriteLabel GroupWriteKind = "label"
	// GroupWriteAssignee replaces the card's assignee.
	GroupWriteAssignee GroupWriteKind = "assignee"
	// GroupWriteType assigns the card the type named TypeName, resolved to a visible type
	// by the handler — PlanGroupMove itself knows nothing of what types exist.
	GroupWriteType GroupWriteKind = "type"
)

// GroupWrite is the single edit a group move performs. It names the field itself — the
// grouping value is not stored anywhere else, so moving between groups IS editing it.
type GroupWrite struct {
	Kind GroupWriteKind
	// Prefix is the label namespace to clear before adding, so a card never carries two
	// epic: labels. Empty for an assignee or type move.
	Prefix string
	// Label is the label to apply; empty means the card is moving into the empty-value
	// group, so the namespace is cleared and nothing is added.
	Label string
	// Assignee is the login to assign; empty means clear the assignees.
	Assignee string
	// TypeName is the target type's name for a GroupWriteType move, lower-cased; empty
	// clears the issue's type.
	TypeName string
}

// PlanGroupMove resolves a group move into the one field edit it performs, or refuses it.
//
// A group move is REFUSED when grouping is off, because there is nothing to write. The
// refusal carries what to do about it.
func PlanGroupMove(grouping Grouping, groupKey string) (GroupWrite, error) {
	switch grouping {
	case GroupType:
		return GroupWrite{Kind: GroupWriteType, TypeName: strings.ToLower(strings.TrimSpace(groupKey))}, nil
	case GroupEpic:
		return GroupWrite{Kind: GroupWriteLabel, Prefix: EpicLabelPrefix, Label: prefixedLabel(EpicLabelPrefix, groupKey)}, nil
	case GroupAssignee:
		return GroupWrite{Kind: GroupWriteAssignee, Assignee: strings.TrimSpace(groupKey)}, nil
	}
	return GroupWrite{}, &hub_model.Error{
		Message: "the board is not grouped, so a group move has no field to write",
		SuggestedAction: "Group the board by type, assignee or epic first — a group is the grouping value, and with grouping off there is only one group. " +
			"Moving a card between COLUMNS works with grouping off.",
	}
}

func prefixedLabel(prefix, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return prefix + key
}
