// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"gitea.dev/models/delivery"
	"gitea.dev/modules/json"
	delivery_service "gitea.dev/services/delivery"
	"gitea.dev/services/delivery/query"
)

// APIVersion is the fork's own API version. Gitea's swagger group at routers/api/v1 is
// untouched; this namespace publishes its own document (F3, I15).
const APIVersion = "1.0.0"

// BasePath is where the namespace mounts (F3).
const BasePath = "/api/delivery/v1"

// Param is one documented request parameter.
type Param struct {
	Name        string   `json:"name"`
	In          string   `json:"in"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Enum        []string `json:"enum,omitempty"`
	Required    bool     `json:"required,omitempty"`
}

// Operation is the contract for one endpoint. The document is generated from these, so an
// endpoint cannot be served without being documented: Routes mounts operations, not
// handlers (I15, I16).
type Operation struct {
	ID          string
	Method      string
	Path        string
	Summary     string
	Description string
	Tag         string
	PathParams  []Param
	Query       *query.Spec // list endpoints derive their grammar parameters from this
	Response    string      // component schema name
	ResponseIs  string      // "array" or "object"
	// Body is the request body, one Param per member, for the operations that take one.
	// The CLI's generated request layer renders each as a flag, so a body member cannot be
	// published without a way to send it (K2, K7).
	Body []Param
	// CLINames overrides the command name derived from ID, and may name more than one:
	// deploy and rollback are the same operation, because rolling back is deploying a prior
	// release tag rather than a second code path. Empty means the derived name.
	CLINames []string
}

// GrammarParams renders the section I grammar for the operation's resource. The grammar is
// declared once, in services/delivery/query; a resource restates only its whitelists (I2).
func (op *Operation) GrammarParams() []Param {
	if op.Query == nil {
		return nil
	}
	spec := *op.Query
	var params []Param
	for _, f := range spec.Fields {
		params = append(params, Param{
			Name:        f.Name,
			In:          "query",
			Type:        openAPIType(f.Kind),
			Description: fmt.Sprintf("Filter on %s. Bare form means the eq operator (I3).", f.Name),
		})
		for _, op := range fieldOps(f) {
			params = append(params, Param{
				Name:        fmt.Sprintf("%s[%s]", f.Name, op),
				In:          "query",
				Type:        openAPIType(f.Kind),
				Description: fmt.Sprintf("Filter %s with the %s operator. Repeating the field ANDs the conditions (I3).", f.Name, op),
			})
		}
	}
	if len(spec.SearchFields) > 0 {
		params = append(params, Param{
			Name: "q", In: "query", Type: "string",
			Description: "Free-text search over " + strings.Join(spec.SearchFields, ", ") + " (I10).",
		})
	}
	if len(spec.SortFields) > 0 {
		params = append(params,
			Param{
				Name: "sort_by", In: "query", Type: "string", Enum: spec.SortFields,
				Description: "Sort field. Every sort is tie-broken on the primary key (I5).",
			},
			Param{
				Name: "order", In: "query", Type: "string", Enum: []string{query.OrderAsc, query.OrderDesc},
				Description: "Sort direction (I5).",
			})
	}
	params = append(params, Param{
		Name: "limit", In: "query", Type: "integer",
		Description: fmt.Sprintf("Page size. Defaults to %d, caps at %d (I7).", query.DefaultLimit, query.MaxLimit),
	})
	if spec.Paging == query.PagingCursor {
		params = append(params, Param{
			Name: "cursor", In: "query", Type: "string",
			Description: "Opaque cursor from a previous response's next token. Append-only resources page by cursor, never by page (I6, I8).",
		})
	} else {
		params = append(params, Param{
			Name: "page", In: "query", Type: "integer",
			Description: "1-based page. Responses carry X-Total-Count and an RFC 5988 Link header (I7).",
		})
	}
	if len(spec.Expands) > 0 {
		params = append(params, Param{
			Name: "expand", In: "query", Type: "string", Enum: spec.Expands,
			Description: fmt.Sprintf("Comma-separated sub-resources, one level deep, at most %d (I9).", query.MaxExpands),
		})
	}
	return params
}

func fieldOps(f query.Field) []string {
	ops := f.Ops
	if len(ops) == 0 {
		ops = f.Kind.DefaultOps()
	}
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		if op != query.OpEq {
			out = append(out, string(op))
		}
	}
	return out
}

func openAPIType(k query.Kind) string {
	switch k {
	case query.KindInt, query.KindTime:
		return "integer"
	case query.KindBool:
		return "boolean"
	}
	return "string"
}

// componentSchemas are the response shapes the document publishes.
var componentSchemas = map[string]any{
	"Environment": objectSchema(map[string]any{
		"id":                 prop("integer", "Primary key."),
		"repo_id":            prop("integer", "Repository the environment belongs to; 0 is the instance-wide default set."),
		"name":               prop("string", "Environment name, lower-cased."),
		"sort_order":         prop("integer", "Render order. Order is configuration; the model expresses no sequence."),
		"approval_policy":    enumProp("Approval policy. Defaults to none, so adding the fork changes no existing behaviour.", delivery.ApprovalPolicies),
		"required_approvals": prop("integer", "Approvals a held deploy needs. Defaults to 1."),
		"created_unix":       prop("integer", "Creation time, unix seconds."),
		"updated_unix":       prop("integer", "Last update, unix seconds."),
	}, "id", "repo_id", "name", "sort_order", "approval_policy", "required_approvals"),
	"SecretName": objectSchema(map[string]any{
		"name":        prop("string", "Secret name. A secret VALUE is never readable over any endpoint at any scope (I12)."),
		"repo_id":     prop("integer", "Repository the secret belongs to."),
		"environment": prop("string", "Environment the secret is scoped to; empty means unscoped."),
		"scoped":      prop("boolean", "Whether an environment scope is configured for this secret."),
	}, "name", "repo_id", "environment", "scoped"),
	"Repository": objectSchema(map[string]any{
		"id":        prop("integer", "Repository id."),
		"owner":     prop("string", "Owner name."),
		"name":      prop("string", "Repository name."),
		"full_name": prop("string", "owner/name."),
	}, "id", "owner", "name", "full_name"),
	"Deployment": objectSchema(map[string]any{
		"id":           prop("integer", "Primary key."),
		"repo_id":      prop("integer", "Repository the deployment belongs to."),
		"environment":  prop("string", "Environment deployed to, lower-cased."),
		"release_tag":  prop("string", "Release tag deployed. Releases own version identity; no version string is parsed (D1)."),
		"sha":          prop("string", "Commit the run built."),
		"branch":       prop("string", "Branch, when the run was not dispatched against a tag."),
		"run_id":       prop("integer", "Gitea's own Actions run id."),
		"run_url":      prop("string", "Link to the run."),
		"status":       prop("string", "Run status at the moment the deployment was first recorded. Written once, never updated: the current state of a cell is projected from the audit log (E3, E5)."),
		"created_unix": prop("integer", "When the row was appended, unix seconds."),
		"release":      prop("object", "The release, when ?expand=release was asked for (I9)."),
		"audit":        arrayProp("object", "The run's audit events, when ?expand=audit was asked for (I9)."),
		"approval":     prop("object", "The approval gate's hold on this run, when ?expand=approval was asked for (I9)."),
	}, "id", "repo_id", "environment", "release_tag", "run_id", "status", "created_unix"),
	"AuditEvent": objectSchema(map[string]any{
		"id":            prop("integer", "Primary key."),
		"event":         enumProp("What happened.", delivery.AuditEvents),
		"occurred_unix": prop("integer", "When it happened, UTC unix seconds."),
		"actor_id":      prop("integer", "Gitea user id of the actor."),
		"actor_login":   prop("string", "Actor login, denormalized so deleting the user from Gitea does not erase who deployed (E5)."),
		"repo_id":       prop("integer", "Repository the event belongs to."),
		"environment":   prop("string", "Environment, lower-cased."),
		"release_tag":   prop("string", "Release tag."),
		"sha":           prop("string", "Commit the run built."),
		"branch":        prop("string", "Branch, when the run was not dispatched against a tag."),
		"run_id":        prop("integer", "Gitea's own Actions run id, the evidence for the event."),
		"run_url":       prop("string", "Link to the run."),
		"source":        enumProp("Where the event came from.", delivery.AuditSources),
		"created_unix":  prop("integer", "When the row was appended, unix seconds."),
	}, "id", "event", "occurred_unix", "actor_id", "actor_login", "repo_id", "environment", "release_tag", "run_id", "source"),
	"Release": objectSchema(map[string]any{
		"id":            prop("integer", "Release id."),
		"repo_id":       prop("integer", "Repository the release belongs to."),
		"tag_name":      prop("string", "Release tag, the identity a deployment points at (D1)."),
		"title":         prop("string", "Release title."),
		"target":        prop("string", "The release's own commitish. The deploy job posts its commit status against this SHA (D2)."),
		"sha":           prop("string", "Commit the tag resolves to."),
		"url":           prop("string", "Link to the release."),
		"is_prerelease": prop("boolean", "Whether the release is marked as a prerelease."),
		"created_unix":  prop("integer", "Creation time, unix seconds."),
		"artifacts":     arrayProp("object", "The release's attachments (E9)."),
		"deployments":   arrayProp("object", "Deployments of this release, when ?expand=deployments was asked for (I9)."),
	}, "id", "repo_id", "tag_name", "target", "url", "created_unix"),
	"GridRow": objectSchema(map[string]any{
		"repo_id":        prop("integer", "Repository the release belongs to."),
		"repo_full_name": prop("string", "owner/name."),
		"release_tag":    prop("string", "The release this row renders."),
		"release_url":    prop("string", "Link to the release; a release row opens the release (E8)."),
		"created_unix":   prop("integer", "Release creation time, unix seconds."),
		"cells":          arrayProp("object", "One cell per environment, in configured order (E7). Each carries environment, state, symbol, successes, run_id, run_url and occurred_unix; a cell opens its run (E8)."),
	}, "repo_id", "repo_full_name", "release_tag", "cells"),
	"Promotion": objectSchema(map[string]any{
		"repo_id":                  prop("integer", "Repository the deploy targets."),
		"repo_full_name":           prop("string", "owner/name."),
		"environment":              prop("string", "Target environment, lower-cased."),
		"release_tag":              prop("string", "Release tag being deployed. Rolling back names a prior tag; it is the same request (E14)."),
		"is_prerelease":            prop("boolean", "Whether the release is marked as a prerelease. Prereleases reach only the configured prerelease environments."),
		"currently_live":           prop("string", "The release live in the target environment right now, empty when nothing has ever succeeded there. The confirm step names it before anything is dispatched (E14)."),
		"is_rollback":              prop("boolean", "Whether the target release predates what is live there. A label on the request, never a second code path."),
		"predecessor":              prop("string", "The environment this one names as its predecessor, empty when it names none (E17)."),
		"predecessor_state":        enumProp("What the predecessor has done with this release: none, never, held or live.", []string{"none", "never", "held", "live"}),
		"outcome":                  enumProp("The sequence rule's decision: proceed, warn, override or refuse (E17).", []string{"proceed", "warn", "override", "refuse"}),
		"message":                  prop("string", "What the decision was, in words, for the confirm step to show."),
		"suggested_action":         prop("string", "What to do about it. Every decision carries one (A21)."),
		"requires_override_reason": prop("boolean", "Whether a confirmed deploy must carry override_reason. The reason is written to the audit log (E17)."),
		"workflow_id":              prop("string", "The workflow file the deploy dispatches, named for the environment (D4)."),
		"ref":                      prop("string", "The ref dispatched. Deploy and rollback compose the identical request and differ only here."),
		"confirmed":                prop("boolean", "Whether this call dispatched. False on the first of E14's two steps."),
		"override_reason":          prop("string", "The reason given for bypassing the sequence rule."),
		"run_id":                   prop("integer", "Gitea's own Actions run id, once dispatched."),
		"run_url":                  prop("string", "Link to the run, once dispatched."),
	}, "repo_id", "repo_full_name", "environment", "release_tag", "outcome", "predecessor_state", "confirmed"),
	"Run": objectSchema(map[string]any{
		"id":               prop("integer", "Gitea's own Actions run id."),
		"repo_id":          prop("integer", "Repository the run belongs to."),
		"repo_full_name":   prop("string", "owner/name, so the row links out to Gitea's own repository page (P1)."),
		"index":            prop("integer", "The run's per-repository number, as Gitea's own run page shows it."),
		"title":            prop("string", "Run title."),
		"workflow_id":      prop("string", "The workflow file that produced the run."),
		"event":            prop("string", "The webhook event that caused the run."),
		"ref":              prop("string", "The commit, branch or tag that caused the run."),
		"commit_sha":       prop("string", "Commit the run built."),
		"status":           enumProp("Run state, as the overview's tiles group them. Filter on this name, never on Gitea's internal integer (P2, P8).", delivery_service.RunStateNames()),
		"run_url":          prop("string", "Link to Gitea's own run page. The overview duplicates no Gitea page (P1, P3)."),
		"created_unix":     prop("integer", "When the run was created, unix seconds."),
		"started_unix":     prop("integer", "When the run started, unix seconds; 0 if it has not."),
		"stopped_unix":     prop("integer", "When the run stopped, unix seconds; 0 if it has not."),
		"duration_seconds": prop("integer", "stopped minus started. An unfinished run contributes zero rather than a negative duration."),
	}, "id", "repo_id", "repo_full_name", "workflow_id", "status", "created_unix"),
	"RepoStat": objectSchema(map[string]any{
		"repo_id":                  prop("integer", "Repository id."),
		"repo_full_name":           prop("string", "owner/name."),
		"runs":                     prop("integer", "Runs in the window."),
		"successes":                prop("integer", "Runs that succeeded."),
		"failures":                 prop("integer", "Runs that failed."),
		"success_rate":             prop("number", "Successes over runs that reached a result. A queue of pending runs does not depress it."),
		"average_duration_seconds": prop("integer", "Mean duration over the runs that finished."),
	}, "repo_id", "repo_full_name", "runs", "success_rate", "average_duration_seconds"),
	"WorkflowStat": objectSchema(map[string]any{
		"repo_id":                  prop("integer", "Repository id."),
		"repo_full_name":           prop("string", "owner/name."),
		"workflow_id":              prop("string", "The workflow file."),
		"runs":                     prop("integer", "Runs in the window."),
		"successes":                prop("integer", "Runs that succeeded."),
		"failures":                 prop("integer", "Runs that failed."),
		"success_rate":             prop("number", "Successes over runs that reached a result."),
		"average_duration_seconds": prop("integer", "Mean duration over the runs that finished."),
		"disabled":                 prop("boolean", "Whether the repository has this workflow disabled, read from Gitea's own Actions unit configuration."),
	}, "repo_id", "workflow_id", "runs", "success_rate", "average_duration_seconds", "disabled"),
	"Summary": objectSchema(map[string]any{
		"window":                 prop("object", "The half-open window the figures cover: from_unix, to_unix and days."),
		"total_runs":             prop("integer", "Runs in the window."),
		"runs":                   prop("object", "Run count per state: "+strings.Join(delivery_service.RunStateNames(), ", ")+" (P2)."),
		"success_rate":           prop("number", "Successes over runs that reached a result."),
		"total_duration_seconds": prop("integer", "Summed run duration."),
		"active_repositories":    prop("integer", "Repositories with at least one run in the window."),
		"inactive_repositories":  prop("integer", "Repositories the viewer can see that had no run in the window."),
		"active_workflows":       prop("integer", "Distinct (repository, workflow file) pairs that ran in the window."),
		"disabled_workflows":     prop("integer", "Workflow files the viewer's repositories have disabled."),
	}, "window", "total_runs", "runs", "success_rate", "total_duration_seconds",
		"active_repositories", "inactive_repositories", "active_workflows", "disabled_workflows"),
	"Overview": objectSchema(map[string]any{
		"summary":   prop("object", "The selected window's Summary."),
		"previous":  prop("object", "The previous window of equal length, shown beside each tile for comparison (P2)."),
		"truncated": prop("boolean", "True when the window held more runs than one aggregate reads, so the numbers are a floor. A silently capped aggregate would be a wrong number that does not say so."),
	}, "summary", "previous", "truncated"),
	"TrendPoint": objectSchema(map[string]any{
		"date":                     prop("string", "The UTC day, YYYY-MM-DD."),
		"day_unix":                 prop("integer", "Start of that UTC day, unix seconds."),
		"runs":                     prop("integer", "Runs created that day."),
		"successes":                prop("integer", "Runs that succeeded."),
		"failures":                 prop("integer", "Runs that failed."),
		"average_duration_seconds": prop("integer", "Mean duration over the runs that finished."),
		"deployments":              prop("integer", "Deployments appended that day, read from the fork's own table so both dashboards share one source of truth (P5, E3)."),
	}, "date", "day_unix", "runs", "successes", "failures", "average_duration_seconds", "deployments"),
	"Approval": objectSchema(map[string]any{
		"id":                 prop("integer", "Primary key."),
		"repo_id":            prop("integer", "Repository the held run belongs to."),
		"environment":        prop("string", "Environment the held job declares, lower-cased."),
		"run_id":             prop("integer", "Gitea's own Actions run id."),
		"job_id":             prop("integer", "Gitea's own Actions job id. The gate holds exactly this job (F5e)."),
		"release_tag":        prop("string", "Release tag the run was dispatched at; empty when it was dispatched against a branch."),
		"sha":                prop("string", "Commit the run builds."),
		"run_url":            prop("string", "Link to the held run."),
		"requester_id":       prop("integer", "Gitea user id of whoever asked for the deploy."),
		"requester_login":    prop("string", "Requester login, denormalized so deleting the user does not erase who asked."),
		"created_unix":       prop("integer", "When the hold was recorded, unix seconds."),
		"state":              enumProp("Projected over the append-only audit log, never a stored column (E15).", delivery.ApprovalStates),
		"approval_policy":    enumProp("The environment's live approval policy.", delivery.ApprovalPolicies),
		"approvals_count":    prop("integer", "Distinct approvers so far, counted under the environment's policy."),
		"required_approvals": prop("integer", "Approvals this deploy needs before the job is assigned."),
		"age_seconds":        prop("integer", "How long the deploy has been held (E16)."),
		"can_approve":        prop("boolean", "Whether the calling user may approve or reject, by the same check the endpoint enforces (SC 21)."),
		"deployment":         prop("object", "The deployment row for this run, when ?expand=deployment was asked for (I9)."),
	}, "id", "repo_id", "environment", "run_id", "job_id", "state", "approvals_count", "required_approvals", "created_unix", "can_approve"),
	"Board": objectSchema(map[string]any{
		"repo_id":        prop("integer", "Repository the board belongs to."),
		"repo_full_name": prop("string", "owner/name."),
		"project_id":     prop("integer", "Gitea's own project id. An epic sync records it as .board.number in the epic's sync-manifest.json."),
		"title":          prop("string", "The board's title."),
		"group_by":       enumProp("The active lane grouping. A view setting, never stored on the project (O2).", delivery_service.Groupings),
		"columns":        arrayProp("object", "Gitea's own project columns, in its own order. Each carries column_id, title, color and default."),
		"lanes": arrayProp("object", "The horizontal lanes Gitea does not model (O1). Each carries key, label, is_empty_value, "+
			"cards and one entry per column, so the result is a rectangle. The lane whose is_empty_value is true holds the "+
			"issues with no value for the active grouping (O3)."),
		"can_write":      prop("boolean", "Whether the caller may move a card between columns, by the same check that endpoint enforces."),
		"can_edit_issue": prop("boolean", "Whether the caller may move a card between lanes, which edits the issue's own label or assignee."),
	}, "repo_id", "repo_full_name", "project_id", "group_by", "columns", "lanes", "can_write", "can_edit_issue"),
	"Timeline": objectSchema(map[string]any{
		"repo_id":        prop("integer", "Repository the chart covers."),
		"repo_full_name": prop("string", "owner/name."),
		"bars": arrayProp("object", "One bar per issue ccpm manages. Each carries issue_id, number, title, url, epic, milestone, "+
			"start_unix, end_unix, start_source, end_source, end_inferred and is_closed. start_source is one of "+
			strings.Join(delivery_service.StartSources, ", ")+" and end_source one of "+
			strings.Join(delivery_service.EndSources, ", ")+"; end_inferred marks a bar whose end is an estimate rather than a record (O8)."),
		"arrows": arrayProp("object", "Dependency edges: from_issue_id, to_issue_id, kind and enforced. kind is one of "+
			strings.Join(delivery_service.ArrowKinds, ", ")+" — a hard gate the forge acts on, or a sequencing hint it does not (O9, N9)."),
		"spans": arrayProp("object", "Epic and milestone rows spanning earliest start to latest end of their children, with "+
			"ccpm's own task-close percentage as progress (O11)."),
		"unmanaged": arrayProp("object", "Issues with no bar, each with the reason and a suggested action. An issue ccpm does "+
			"not manage has no start to draw and is listed rather than given a fabricated bar (O10)."),
		"truncated": prop("boolean", "True when the issue set hit the page limit, so the chart is a prefix. A silently capped chart would be a wrong picture that does not say so."),
	}, "repo_id", "repo_full_name", "bars", "arrows", "spans", "unmanaged", "truncated"),
	"Error": objectSchema(map[string]any{
		"code":             prop("string", "Machine-readable rejection code."),
		"message":          prop("string", "What went wrong."),
		"suggested_action": prop("string", "What to do about it. Every error carries one (A21)."),
		"parameter":        prop("string", "The offending parameter, when the rejection names one."),
		"accepted":         arrayProp("string", "What the endpoint would have accepted instead."),
	}, "code", "message", "suggested_action"),
}

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

func arrayProp(typ, desc string) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": typ}}
}

func enumProp(desc string, values []string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

func objectSchema(props map[string]any, required ...string) map[string]any {
	sort.Strings(required)
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// OpenAPI renders the OpenAPI 3 document for the namespace. Map keys marshal in sorted
// order, so regenerating an unchanged registry is byte-identical and the diff gate of I16
// shows only real changes.
func OpenAPI() ([]byte, error) {
	paths := map[string]any{}
	for _, op := range Operations() {
		params := make([]any, 0, 16)
		for _, p := range op.PathParams {
			params = append(params, paramObject(p, true))
		}
		for _, p := range op.GrammarParams() {
			params = append(params, paramObject(p, p.Required))
		}
		var schema any = map[string]any{"$ref": "#/components/schemas/" + op.Response}
		if op.ResponseIs == "array" {
			schema = map[string]any{"type": "array", "items": schema}
		}
		operation := map[string]any{
			"operationId": op.ID,
			"summary":     op.Summary,
			"description": op.Description,
			"tags":        []string{op.Tag},
			"parameters":  params,
			"responses": map[string]any{
				"200": map[string]any{
					"description": "Success.",
					"content":     map[string]any{"application/json": map[string]any{"schema": schema}},
				},
				"400": errorResponse("The request was refused, naming the offender and what is accepted (I4)."),
				"403": errorResponse("The calling user is not permitted, by Gitea's own permission check (I13)."),
				"404": errorResponse("No such resource."),
			},
		}
		if len(op.Body) > 0 {
			operation["requestBody"] = requestBody(op.Body)
		}
		entry, ok := paths[op.Path].(map[string]any)
		if !ok {
			entry = map[string]any{}
			paths[op.Path] = entry
		}
		entry[strings.ToLower(op.Method)] = operation
	}

	doc := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "Gitea delivery API",
			"version":     APIVersion,
			"description": "The fork's own namespace. Every view is a client of an endpoint here (E18, I14); Gitea's swagger group at /api/v1 is untouched (F3, I15).",
		},
		"servers":    []any{map[string]any{"url": BasePath}},
		"paths":      paths,
		"components": map[string]any{"schemas": componentSchemas},
	}
	var buf bytes.Buffer
	if err := encodeDeterministic(&buf, doc, ""); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// encodeDeterministic renders the document with map keys in sorted order.
//
// It exists because encoding/json/v2 — what modules/json wraps at the pin — does not order
// map keys, so marshalling the document twice produced two different byte strings. A
// generated artifact whose bytes move between runs cannot be diff-gated (I16): the gate
// would fail for no reason and be turned off.
func encodeDeterministic(buf *bytes.Buffer, value any, indent string) error {
	inner := indent + "  "
	switch v := value.(type) {
	case map[string]any:
		if len(v) == 0 {
			buf.WriteString("{}")
			return nil
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteString("{\n")
		for i, k := range keys {
			buf.WriteString(inner)
			if err := encodeDeterministic(buf, k, inner); err != nil {
				return err
			}
			buf.WriteString(": ")
			if err := encodeDeterministic(buf, v[k], inner); err != nil {
				return err
			}
			if i < len(keys)-1 {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
		buf.WriteString(indent + "}")
		return nil
	case []any:
		return encodeArray(buf, v, indent)
	case []string:
		items := make([]any, len(v))
		for i, s := range v {
			items[i] = s
		}
		return encodeArray(buf, items, indent)
	}
	scalar, err := json.Marshal(value)
	if err != nil {
		return err
	}
	buf.Write(scalar)
	return nil
}

func encodeArray(buf *bytes.Buffer, items []any, indent string) error {
	if len(items) == 0 {
		buf.WriteString("[]")
		return nil
	}
	inner := indent + "  "
	buf.WriteString("[\n")
	for i, item := range items {
		buf.WriteString(inner)
		if err := encodeDeterministic(buf, item, inner); err != nil {
			return err
		}
		if i < len(items)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString(indent + "]")
	return nil
}

// requestBody renders an operation's body from the same Param list the CLI generates its
// flags from, so the document and the client cannot disagree about what may be sent.
func requestBody(members []Param) map[string]any {
	props := map[string]any{}
	required := make([]string, 0, len(members))
	for _, m := range members {
		schema := map[string]any{"type": m.Type, "description": m.Description}
		if len(m.Enum) > 0 {
			schema["enum"] = m.Enum
		}
		props[m.Name] = schema
		if m.Required {
			required = append(required, m.Name)
		}
	}
	sort.Strings(required)
	return map[string]any{
		"required": true,
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{"type": "object", "properties": props, "required": required},
			},
		},
	}
}

func errorResponse(desc string) map[string]any {
	return map[string]any{
		"description": desc,
		"content": map[string]any{
			"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Error"}},
		},
	}
}

func paramObject(p Param, required bool) map[string]any {
	schema := map[string]any{"type": p.Type}
	if len(p.Enum) > 0 {
		schema["enum"] = p.Enum
	}
	return map[string]any{
		"name":        p.Name,
		"in":          p.In,
		"description": p.Description,
		"required":    required,
		"schema":      schema,
	}
}

// Schemas exposes the published component schemas, so a generator can render a table from
// the same declaration the document publishes.
func Schemas() map[string]map[string]any {
	out := make(map[string]map[string]any, len(componentSchemas))
	for name, schema := range componentSchemas {
		if m, ok := schema.(map[string]any); ok {
			out[name] = m
		}
	}
	return out
}
