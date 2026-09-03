// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"strings"

	hubapi "gitea.dev/routers/api/hub"
	planning_service "gitea.dev/services/planning"
)

// APIVersion is the fork's own API version. Gitea's swagger group at routers/api/v1 is
// untouched; this namespace publishes its own document.
const APIVersion = "1.0.0"

// componentSchemas are the response shapes this area's document publishes.
var componentSchemas = map[string]any{
	"Board": hubapi.ObjectSchema(map[string]any{
		"repo_id":        hubapi.Prop("integer", "Repository the board belongs to."),
		"repo_full_name": hubapi.Prop("string", "owner/name."),
		"project_id":     hubapi.Prop("integer", "Gitea's own project id. An epic sync records it as .board.number in the epic's sync-manifest.json."),
		"title":          hubapi.Prop("string", "The board's title."),
		"group_by":       hubapi.EnumProp("The active grouping. A view setting, never stored on the project.", planning_service.Groupings),
		"columns":        hubapi.ArrayProp("object", "Gitea's own project columns, in its own order. Each carries column_id, title, color and default."),
		"groups": hubapi.ArrayProp("object", "The horizontal groups Gitea does not model. Each carries key, label, is_empty_value, "+
			"cards and one entry per column, so the result is a rectangle. The group whose is_empty_value is true holds the "+
			"issues with no value for the active grouping."),
		"types": hubapi.ArrayProp("object", "The types visible from this repository, nearest scope shadowing by name — what a card's "+
			"type picker offers. Cards carry the assignment itself: type, type_id, type_color and type_icon."),
		"can_write":      hubapi.Prop("boolean", "Whether the caller may move a card between columns, by the same check that endpoint enforces."),
		"can_edit_issue": hubapi.Prop("boolean", "Whether the caller may move a card between groups, which edits the issue's own label, assignee or assigned type."),
	}, "repo_id", "repo_full_name", "project_id", "group_by", "columns", "groups", "types", "can_write", "can_edit_issue"),
	"Roadmap": hubapi.ObjectSchema(map[string]any{
		"repo_id":        hubapi.Prop("integer", "Repository the chart covers."),
		"repo_full_name": hubapi.Prop("string", "owner/name."),
		"bars": hubapi.ArrayProp("object", "One bar per issue ccpm manages, empty at a rolled-up zoom. Each carries issue_id, number, "+
			"title, url, epic, type, type_id, type_color, type_icon, labels, assignees, milestone, start_unix, end_unix, "+
			"start_source, end_source, end_inferred and is_closed. type comes from the issue's own assignment, not a label. start_source is one of "+
			strings.Join(planning_service.StartSources, ", ")+" and end_source one of "+
			strings.Join(planning_service.EndSources, ", ")+"; end_inferred marks a bar whose end is an estimate rather than a record."),
		"arrows": hubapi.ArrayProp("object", "Dependency edges: from_issue_id, to_issue_id, kind and enforced. kind is one of "+
			strings.Join(planning_service.ArrowKinds, ", ")+" — a hard gate the forge acts on, or a sequencing hint it does not. "+
			"At a rolled-up zoom an edge between two children is re-keyed onto the brackets holding them: from_rollup and to_rollup "+
			"carry the rollup keys as kind:key, while from_issue_id and to_issue_id keep naming the child issues, and an edge with "+
			"both ends in one bracket is dropped because it says nothing about the order of the brackets. Both rollup fields are "+
			"absent at issue zoom, where an edge already joins two drawn bars."),
		"rollups": hubapi.ArrayProp("object", "Epic and milestone rows rolling up earliest start to latest end of their children, with "+
			"ccpm's own task-close percentage as progress. They are computed from their own fetch of every child rather than "+
			"from the bars that got drawn, so an epic is checked against its children even where none is drawn. An epic row "+
			"also carries issue_id, declared_start_unix and declared_end_unix — the epic issue's OWN bar — and "+
			"contains_children, warning and suggested_action when the declared window does not contain the derived one. "+
			"partial marks a row whose fetch hit its cap; such a row publishes progress 0, because a fraction of an unknown "+
			"denominator is not a measurement."),
		"unmanaged": hubapi.ArrayProp("object", "Issues with no bar, each with the reason and a suggested action. An issue ccpm does "+
			"not manage has no start to draw and is listed rather than given a fabricated bar."),
		"group_by": hubapi.EnumProp("The active grouping, reusing the board's own. A view setting, never stored.", planning_service.Groupings),
		"zoom":     hubapi.EnumProp("The depth the chart is read at. At epic or milestone only rollup rows are listed and no bar is drawn.", planning_service.Zooms),
		"groups": hubapi.ArrayProp("object", "The PUBLISHED bars grouped by the board's own group definition. Empty when grouping is off, "+
			"and empty at a rolled-up zoom, which publishes no bars to group. Each group carries key, label, is_empty_value, "+
			"cards and one column holding its bars."),
		"ruler": hubapi.Prop("object", "The time axis: unit, start_unix, end_unix and ticks, each with unix and label. The unit follows "+
			"the range drawn — day up to a fortnight, week up to ten weeks, month up to eighteen months, quarter beyond — while "+
			"the write granularity stays a day at every unit."),
		"milestones": hubapi.ArrayProp("object", "The repository's milestones, which are the rows an issue can be filed under. Each "+
			"carries milestone_id, title, is_closed, start_unix (the recorded schedule, 0 when unset) and end_unix (the milestone's own deadline, 0 when unset)."),
		"truncated": hubapi.Prop("boolean", "True when the issue set hit the page limit, so the chart is a prefix. A silently capped chart would be a wrong picture that does not say so."),
		"types":     hubapi.ArrayProp("object", "The types visible from this repository, nearest scope shadowing by name — what a bar's type picker offers."),
	}, "repo_id", "repo_full_name", "bars", "arrows", "rollups", "unmanaged", "group_by", "zoom", "groups", "ruler", "types", "truncated"),
	"IssueFacets": hubapi.ObjectSchema(map[string]any{
		"issue_id":  hubapi.Prop("integer", "The issue's global id."),
		"number":    hubapi.Prop("integer", "The issue's per-repository number."),
		"repo_id":   hubapi.Prop("integer", "Repository the issue belongs to."),
		"can_write": hubapi.Prop("boolean", "Whether the caller may write the schedule and estimate endpoints below."),
		"schedule": hubapi.Prop("object", "start_unix and start_source, one of "+
			strings.Join(planning_service.StartSources, ", ")+"."),
		"milestone":       hubapi.Prop("object", "The milestone the issue is filed under, or null. Carries id, title, start_unix and due_unix (0 when unset)."),
		"time_estimate":   hubapi.Prop("integer", "Seconds, from Gitea's own time-tracking."),
		"tracked_seconds": hubapi.Prop("integer", "Seconds actually logged, from Gitea's own time-tracking."),
		"type":            hubapi.Prop("object", "The issue's assigned type — type_id, name, color and icon — or null."),
		"types":           hubapi.ArrayProp("object", "The types visible from this issue's repository, what a type picker offers."),
	}, "issue_id", "number", "repo_id", "can_write", "schedule", "milestone", "time_estimate", "tracked_seconds", "type", "types"),
	"IssueType": hubapi.ObjectSchema(map[string]any{
		"id":       hubapi.Prop("integer", "The type's id."),
		"name":     hubapi.Prop("string", "Lower-cased, 1-50 characters."),
		"color":    hubapi.Prop("string", "The type's colour."),
		"icon":     hubapi.Prop("string", "An octicon-* name shipped under public/assets/img/svg."),
		"rank":     hubapi.Prop("integer", "1 (highest) to 9 (lowest)."),
		"sort":     hubapi.Prop("integer", "Tie-breaker within the same rank."),
		"scope":    hubapi.EnumProp("Where the type lives.", []string{planning_service.ScopeInstance, planning_service.ScopeOrg, planning_service.ScopeRepo}),
		"scope_id": hubapi.Prop("integer", "The repository or organization id; 0 for the instance scope."),
	}, "id", "name", "color", "icon", "rank", "sort", "scope", "scope_id"),
	"IssueTypeAssignment": hubapi.ObjectSchema(map[string]any{
		"issue_id": hubapi.Prop("integer", "The issue's global id."),
		"type_id":  hubapi.Prop("integer", "The assigned type's id."),
		"name":     hubapi.Prop("string", "The assigned type's name."),
		"color":    hubapi.Prop("string", "The assigned type's colour."),
		"icon":     hubapi.Prop("string", "The assigned type's icon name."),
		"icon_svg": hubapi.Prop("string", "The icon rendered as svg markup, so a client needs no icon registry of its own."),
	}, "issue_id", "type_id", "name", "color", "icon", "icon_svg"),
	"MilestoneSchedule": hubapi.ObjectSchema(map[string]any{
		"milestone_id": hubapi.Prop("integer", "The milestone's id."),
		"title":        hubapi.Prop("string", "The milestone's title."),
		"start_unix":   hubapi.Prop("integer", "The recorded start, 0 when unset."),
		"due_unix":     hubapi.Prop("integer", "The milestone's own deadline, 0 when unset."),
	}, "milestone_id", "title", "start_unix", "due_unix"),
	"Error": hubapi.ErrorSchema(),
}

// OpenAPI renders the OpenAPI 3 document for the planning namespace.
func OpenAPI() ([]byte, error) {
	return hubapi.BuildOpenAPI(BasePath, "Gitea planning API",
		"The fork's own planning namespace: board, roadmap and their writes. Gitea's swagger group at /api/v1 is untouched.",
		APIVersion, Operations(), componentSchemas)
}

// Schemas exposes the published component schemas, so a generator can render a table from
// the same declaration the document publishes.
func Schemas() map[string]map[string]any { return hubapi.SchemasFrom(componentSchemas) }
