// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"sort"
	"strings"

	delivery_model "gitea.dev/models/delivery"
)

// The board renders Gitea's project columns vertically and adds horizontal lanes, a
// dimension Gitea does not model: project.Column carries ID, Title, Default, Sorting,
// Color, ProjectID, CreatorID and timestamps and no lane, row or group field (verified in
// models/project/column.go). Lanes are therefore a RENDERING over rows the Projects API
// already returns — no schema change, no migration and no fork change.
//
// Everything in this file is pure: it takes reduced structs and returns reduced structs, so
// lane assignment is tested with no database and no server.

// Grouping is the lane dimension, chosen at view time. It is never stored on the project,
// so two people may group the same board differently.
type Grouping string

const (
	// GroupNone renders the board with a single lane, which is Gitea's own board.
	GroupNone Grouping = "none"
	// GroupType groups by ccpm's type:<t> label.
	GroupType Grouping = "type"
	// GroupAssignee groups by the issue's assignee.
	GroupAssignee Grouping = "assignee"
	// GroupEpic groups by ccpm's epic:<name> label.
	GroupEpic Grouping = "epic"
)

// Groupings is the accepted set, in declaration order. The API rejects anything else
// naming this list, so an unknown grouping is refused rather than silently treated as none.
var Groupings = []string{string(GroupType), string(GroupAssignee), string(GroupEpic), string(GroupNone)}

// The label prefixes ccpm writes. They are spelled once here.
const (
	TypeLabelPrefix = "type:"
	EpicLabelPrefix = "epic:"
)

// Lane keys and labels for the explicit empty-value lane. An issue with no value for
// the active grouping lands here; nothing disappears from a board because a field is unset.
const emptyLaneKey = ""

// singleLaneLabel names the one lane grouping "none" renders.
const singleLaneLabel = "All issues"

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

// Card is one issue on the board, reduced to what lane assignment depends on.
type Card struct {
	IssueID  int64  `json:"issue_id"`
	Number   int64  `json:"number"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	ColumnID int64  `json:"column_id"`
	Sorting  int64  `json:"sorting"`
	// Labels and Assignees are the grouping fields. A lane move edits one of them.
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

// LaneColumn is one column within one lane: the cells of the board's grid.
type LaneColumn struct {
	ColumnID int64  `json:"column_id"`
	Title    string `json:"title"`
	Cards    []Card `json:"cards"`
}

// Lane is one horizontal row of the board.
type Lane struct {
	// Key is the grouping value itself — the text a lane move writes. Empty is the
	// empty-value lane.
	Key   string `json:"key"`
	Label string `json:"label"`
	// IsEmptyValue marks the issues with no value for the active
	// grouping, shown rather than dropped.
	IsEmptyValue bool         `json:"is_empty_value"`
	Columns      []LaneColumn `json:"columns"`
	Cards        int          `json:"cards"`
}

// emptyLaneLabel names the empty-value lane per grouping, so it reads as a fact about the
// issues rather than as a blank row.
func emptyLaneLabel(g Grouping) string {
	switch g {
	case GroupType:
		return "no type label"
	case GroupAssignee:
		return "unassigned"
	case GroupEpic:
		return "no epic label"
	}
	return singleLaneLabel
}

// labelValue returns the text after prefix on the first matching label, sorted so a card
// carrying two lands in the same lane on every render. Matching is case-insensitive on the
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

// LaneKeyFor is the whole of lane assignment. A row with no value for the active grouping
// returns the empty key, which BuildLanes renders as the explicit empty-value lane.
//
// It takes the two slices rather than a Card so the chart's bars and the board's cards reach
// one definition of a lane.
//
// A row with more than one candidate value lands in the lexicographically first, so neither
// view reshuffles between two renders of the same data.
func LaneKeyFor(labels, assignees []string, grouping Grouping) string {
	switch grouping {
	case GroupType:
		return labelValue(labels, TypeLabelPrefix)
	case GroupEpic:
		return labelValue(labels, EpicLabelPrefix)
	case GroupAssignee:
		if len(assignees) == 0 {
			return emptyLaneKey
		}
		sorted := append([]string(nil), assignees...)
		sort.Strings(sorted)
		return sorted[0]
	}
	return emptyLaneKey
}

// BuildLanes renders the board: every lane carries every column, in the Projects API's own
// order, so the result is a rectangle a template can lay out without knowing the grouping.
//
// The empty-value lane is always last and is emitted only when it holds a card — except
// under GroupNone, where the single lane IS the board and is always emitted.
func BuildLanes(columns []BoardColumn, cards []Card, grouping Grouping) []Lane {
	byLane := map[string][]Card{}
	order := make([]string, 0, 8)
	for _, card := range cards {
		key := LaneKeyFor(card.Labels, card.Assignees, grouping)
		if _, seen := byLane[key]; !seen {
			order = append(order, key)
		}
		byLane[key] = append(byLane[key], card)
	}
	if grouping == GroupNone {
		byLane = map[string][]Card{emptyLaneKey: cards}
		order = []string{emptyLaneKey}
	}
	if len(order) == 0 {
		order = []string{emptyLaneKey}
		byLane[emptyLaneKey] = nil
	}

	sort.Slice(order, func(i, j int) bool {
		// The empty-value lane is explicit, and it sorts last so the named lanes
		// read first.
		if (order[i] == emptyLaneKey) != (order[j] == emptyLaneKey) {
			return order[j] == emptyLaneKey
		}
		return order[i] < order[j]
	})

	lanes := make([]Lane, 0, len(order))
	for _, key := range order {
		lane := Lane{Key: key, Label: key, IsEmptyValue: key == emptyLaneKey}
		if lane.IsEmptyValue {
			lane.Label = emptyLaneLabel(grouping)
		}
		lane.Columns = make([]LaneColumn, 0, len(columns))
		for _, col := range columns {
			lc := LaneColumn{ColumnID: col.ID, Title: col.Title, Cards: []Card{}}
			for _, card := range byLane[key] {
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
			lane.Cards += len(lc.Cards)
			lane.Columns = append(lane.Columns, lc)
		}
		lanes = append(lanes, lane)
	}
	return lanes
}

// timelineLaneColumn is the one column the chart's lanes carry. A chart has no columns of its
// own, so BuildLanes lays every bar of a lane into a single cell and the result is still the
// rectangle a template can walk.
const timelineLaneColumn = "bars"

// TimelineLanes groups the chart's bars through the board's own lane definition, so a lane on
// the chart and a lane on the board are the same value read the same way.
func TimelineLanes(bars []Bar, grouping Grouping) []Lane {
	cards := make([]Card, 0, len(bars))
	for _, bar := range bars {
		cards = append(cards, Card{
			IssueID: bar.IssueID, Number: bar.Number, Title: bar.Title, URL: bar.URL,
			Labels: bar.Labels, Assignees: bar.Assignees, IsClosed: bar.IsClosed,
		})
	}
	return BuildLanes([]BoardColumn{{Title: timelineLaneColumn}}, cards, grouping)
}

// LaneWriteKind is what a lane move edits.
type LaneWriteKind string

const (
	// LaneWriteLabel replaces the card's type: or epic: label.
	LaneWriteLabel LaneWriteKind = "label"
	// LaneWriteAssignee replaces the card's assignee.
	LaneWriteAssignee LaneWriteKind = "assignee"
)

// LaneWrite is the single edit a lane move performs. It names the field itself — the
// grouping value is not stored anywhere else, so moving between lanes IS editing it.
type LaneWrite struct {
	Kind LaneWriteKind
	// Prefix is the label namespace to clear before adding, so a card never carries two
	// type: labels. Empty for an assignee move.
	Prefix string
	// Label is the label to apply; empty means the card is moving into the empty-value
	// lane, so the namespace is cleared and nothing is added.
	Label string
	// Assignee is the login to assign; empty means clear the assignees.
	Assignee string
}

// PlanLaneMove resolves a lane move into the one field edit it performs, or refuses it.
//
// A lane move is REFUSED when grouping is off, because there is nothing to write. The
// refusal carries what to do about it.
func PlanLaneMove(grouping Grouping, laneKey string) (LaneWrite, error) {
	switch grouping {
	case GroupType:
		return LaneWrite{Kind: LaneWriteLabel, Prefix: TypeLabelPrefix, Label: prefixedLabel(TypeLabelPrefix, laneKey)}, nil
	case GroupEpic:
		return LaneWrite{Kind: LaneWriteLabel, Prefix: EpicLabelPrefix, Label: prefixedLabel(EpicLabelPrefix, laneKey)}, nil
	case GroupAssignee:
		return LaneWrite{Kind: LaneWriteAssignee, Assignee: strings.TrimSpace(laneKey)}, nil
	}
	return LaneWrite{}, &delivery_model.Error{
		Message: "the board is not grouped, so a lane move has no field to write",
		SuggestedAction: "Group the board by type, assignee or epic first — a lane is the grouping value, and with grouping off there is only one lane. " +
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
