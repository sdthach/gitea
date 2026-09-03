// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"gitea.dev/modules/json"
	"gitea.dev/services/hub/query"
)

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
// endpoint cannot be served without being documented: an area's Routes call mounts
// operations, not handlers.
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
	// QueryParams are non-grammar query parameters documented alongside the spec's own.
	QueryParams []Param
	// Body is the request body, one Param per member, for the operations that take one.
	// The CLI's generated request layer renders each as a flag, so a body member cannot be
	// published without a way to send it.
	Body []Param
	// CLINames overrides the command name derived from ID, and may name more than one:
	// deploy and rollback are the same operation, because rolling back is deploying a prior
	// release tag rather than a second code path. Empty means the derived name.
	CLINames []string
}

// GrammarParams renders the section I grammar for the operation's resource. The grammar is
// declared once, in services/hub/query; a resource restates only its whitelists.
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
			Description: fmt.Sprintf("Filter on %s. Bare form means the eq operator.", f.Name),
		})
		for _, op := range fieldOps(f) {
			params = append(params, Param{
				Name:        fmt.Sprintf("%s[%s]", f.Name, op),
				In:          "query",
				Type:        openAPIType(f.Kind),
				Description: fmt.Sprintf("Filter %s with the %s operator. Repeating the field ANDs the conditions.", f.Name, op),
			})
		}
	}
	if len(spec.SearchFields) > 0 {
		params = append(params, Param{
			Name: "q", In: "query", Type: "string",
			Description: "Free-text search over " + strings.Join(spec.SearchFields, ", ") + ".",
		})
	}
	if len(spec.SortFields) > 0 {
		params = append(params,
			Param{
				Name: "sort_by", In: "query", Type: "string", Enum: spec.SortFields,
				Description: "Sort field. Every sort is tie-broken on the primary key.",
			},
			Param{
				Name: "order", In: "query", Type: "string", Enum: []string{query.OrderAsc, query.OrderDesc},
				Description: "Sort direction.",
			})
	}
	params = append(params, Param{
		Name: "limit", In: "query", Type: "integer",
		Description: fmt.Sprintf("Page size. Defaults to %d, caps at %d.", query.DefaultLimit, query.MaxLimit),
	})
	if spec.Paging == query.PagingCursor {
		params = append(params, Param{
			Name: "cursor", In: "query", Type: "string",
			Description: "Opaque cursor from a previous response's next token. Append-only resources page by cursor, never by page.",
		})
	} else {
		params = append(params, Param{
			Name: "page", In: "query", Type: "integer",
			Description: "1-based page. Responses carry X-Total-Count and an RFC 5988 Link header.",
		})
	}
	if len(spec.Expands) > 0 {
		params = append(params, Param{
			Name: "expand", In: "query", Type: "string", Enum: spec.Expands,
			Description: fmt.Sprintf("Comma-separated sub-resources, one level deep, at most %d.", query.MaxExpands),
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

// Prop, ArrayProp, EnumProp and ObjectSchema build the response schemas an area's own
// openapi.go declares. They are exported so componentSchemas can live beside the area it
// documents rather than in this shared package.
func Prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

func ArrayProp(typ, desc string) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": typ}}
}

func EnumProp(desc string, values []string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

func ObjectSchema(props map[string]any, required ...string) map[string]any {
	sort.Strings(required)
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// BuildOpenAPI renders the OpenAPI 3 document for one area. Map keys marshal in sorted
// order, so regenerating an unchanged registry is byte-identical and the diff gate shows
// only real changes.
func BuildOpenAPI(basePath, title, description, apiVersion string, ops []*Operation, schemas map[string]any) ([]byte, error) {
	paths := map[string]any{}
	for _, op := range ops {
		params := make([]any, 0, 16)
		for _, p := range op.PathParams {
			params = append(params, paramObject(p, p.Required))
		}
		for _, p := range op.QueryParams {
			params = append(params, paramObject(p, p.Required))
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
				"400": errorResponse("The request was refused, naming the offender and what is accepted."),
				"403": errorResponse("The calling user is not permitted, by Gitea's own permission check."),
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
			"title":       title,
			"version":     apiVersion,
			"description": description,
		},
		"servers":    []any{map[string]any{"url": basePath}},
		"paths":      paths,
		"components": map[string]any{"schemas": schemas},
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
// generated artifact whose bytes move between runs cannot be diff-gated: the gate
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

// ErrorSchema is the response shape every area publishes for a rejection; each area's own
// componentSchemas includes it under "Error" so errorResponse's $ref resolves.
func ErrorSchema() map[string]any {
	return ObjectSchema(map[string]any{
		"code":             Prop("string", "Machine-readable rejection code."),
		"message":          Prop("string", "What went wrong."),
		"suggested_action": Prop("string", "What to do about it. Every error carries one."),
		"parameter":        Prop("string", "The offending parameter, when the rejection names one."),
		"accepted":         ArrayProp("string", "What the endpoint would have accepted instead."),
	}, "code", "message", "suggested_action")
}

// SchemasFrom narrows an area's component schema map to the map[string]map[string]any shape
// a generator renders a table from.
func SchemasFrom(componentSchemas map[string]any) map[string]map[string]any {
	out := make(map[string]map[string]any, len(componentSchemas))
	for name, schema := range componentSchemas {
		if m, ok := schema.(map[string]any); ok {
			out[name] = m
		}
	}
	return out
}
