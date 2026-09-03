// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package query

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Sort orders.
const (
	OrderAsc  = "asc"
	OrderDesc = "desc"
)

// reserved parameter names are the grammar's own; everything else is a filter.
var reserved = []string{"q", "sort_by", "order", "limit", "page", "cursor", "expand", "fields"}

// Filter is one parsed `field[op]=value` condition.
type Filter struct {
	Field  Field
	Op     Op
	Raw    string
	Values []any // one element, except for OpIn
	// Text is the substring an OpContains filter matches. It is a typed field rather than a
	// runtime assertion on Values, so rendering the condition needs no unreachable branch.
	Text string
}

// Sort is the resolved sort, always tie-broken on the primary key.
type Sort struct {
	Column     string
	Order      string
	TieBreaker string // the primary key column
}

// Query is a parsed request.
type Query struct {
	Spec    Spec
	Filters []Filter
	Search  string
	Sort    Sort
	Expand  []string

	Limit  int
	Page   int    // PagingOffset only, 1-based
	Cursor Cursor // PagingCursor only; zero value means "from the start"
}

// Parse reads the grammar out of url values against a resource's whitelists.
// An unknown field, an unknown operator or an unparseable value is refused; a filter
// is never silently dropped.
func Parse(values url.Values, spec Spec) (*Query, *Error) {
	q := &Query{Spec: spec, Limit: DefaultLimit, Page: 1}

	if err := parseFilters(values, spec, q); err != nil {
		return nil, err
	}
	q.Search = strings.TrimSpace(values.Get("q"))
	if err := parseSort(values, spec, q); err != nil {
		return nil, err
	}
	if err := parseExpand(values, spec, q); err != nil {
		return nil, err
	}
	if err := parsePaging(values, spec, q); err != nil {
		return nil, err
	}
	return q, nil
}

func parseFilters(values url.Values, spec Spec, q *Query) *Error {
	names := slices.Sorted(maps(values))
	for _, param := range names {
		if slices.Contains(reserved, param) {
			continue
		}
		name, op, err := splitParam(param, spec)
		if err != nil {
			return err
		}
		field, ok := spec.field(name)
		if !ok {
			return errUnknownField(spec.Resource, name, spec.fieldNames())
		}
		if !field.allows(op) {
			return errOperatorNotAllowed(name, string(op), opNames(field.ops()))
		}
		if op == OpContains && field.Kind != KindString {
			// A substring match has no meaning on a number, a boolean or a timestamp, and
			// letting one through would build a LIKE against a non-text column.
			return errOperatorNotAllowed(name, string(op), opNames(field.ops()))
		}
		// Repeating a field ANDs the conditions.
		for _, raw := range values[param] {
			parsed, err := parseValues(field, op, raw)
			if err != nil {
				return err
			}
			filter := Filter{Field: field, Op: op, Raw: raw, Values: parsed}
			if op == OpContains {
				// Parse has already refused `contains` on a non-string field.
				filter.Text = raw
			}
			q.Filters = append(q.Filters, filter)
		}
	}
	return nil
}

// splitParam reads `field[op]` into its parts. A bare `field` means `eq`.
func splitParam(param string, spec Spec) (string, Op, *Error) {
	open := strings.IndexByte(param, '[')
	if open < 0 {
		return param, OpEq, nil
	}
	if !strings.HasSuffix(param, "]") {
		return "", "", errUnknownOperator(param, param[open+1:], opNames(AllOps))
	}
	name := param[:open]
	opName := param[open+1 : len(param)-1]
	op := Op(opName)
	if !slices.Contains(AllOps, op) {
		// Name the field the caller meant so the message points at the whole parameter.
		if _, ok := spec.field(name); !ok {
			return "", "", errUnknownField(spec.Resource, name, spec.fieldNames())
		}
		return "", "", errUnknownOperator(name, opName, opNames(AllOps))
	}
	return name, op, nil
}

func parseValues(field Field, op Op, raw string) ([]any, *Error) {
	if op != OpIn {
		v, err := parseValue(field, op, raw)
		if err != nil {
			return nil, err
		}
		return []any{v}, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		v, err := parseValue(field, op, strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func parseValue(field Field, op Op, raw string) (any, *Error) {
	switch field.Kind {
	case KindInt:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, errUnparseableValue(field.Name, string(op), raw, field.Kind.String())
		}
		return n, nil
	case KindBool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, errUnparseableValue(field.Name, string(op), raw, field.Kind.String())
		}
		return b, nil
	case KindTime:
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return n, nil
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, errUnparseableValue(field.Name, string(op), raw, field.Kind.String())
		}
		return t.Unix(), nil
	}
	if raw == "" {
		return nil, errUnparseableValue(field.Name, string(op), raw, field.Kind.String())
	}
	return raw, nil
}

func parseSort(values url.Values, spec Spec, q *Query) *Error {
	sortBy := spec.defaultSort()
	order := spec.defaultOrder()

	if raw := values.Get("sort_by"); raw != "" {
		if !slices.Contains(spec.SortFields, raw) {
			return errUnknownSortField(spec.Resource, raw, spec.SortFields)
		}
		sortBy = raw
	}
	if raw := values.Get("order"); raw != "" {
		if raw != OrderAsc && raw != OrderDesc {
			return errUnknownOrder(raw)
		}
		order = raw
	}

	column := sortBy
	if field, ok := spec.field(sortBy); ok {
		column = field.Column
	}
	q.Sort = Sort{Column: column, Order: order, TieBreaker: spec.PrimaryKey}
	return nil
}

func parseExpand(values url.Values, spec Spec, q *Query) *Error {
	raw := values.Get("expand")
	if raw == "" {
		return nil
	}
	var wanted []string
	for name := range strings.SplitSeq(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.Contains(name, ".") {
			return errNestedExpand(name)
		}
		if !slices.Contains(spec.Expands, name) {
			return errUnknownExpand(spec.Resource, name, spec.Expands)
		}
		if !slices.Contains(wanted, name) {
			wanted = append(wanted, name)
		}
	}
	if len(wanted) > MaxExpands {
		return errTooManyExpands(len(wanted), MaxExpands)
	}
	q.Expand = wanted
	return nil
}

func parsePaging(values url.Values, spec Spec, q *Query) *Error {
	if raw := values.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return errBadPagingParam("limit", raw, pagingName(spec.Paging))
		}
		q.Limit = min(n, MaxLimit)
	}

	switch spec.Paging {
	case PagingCursor:
		if raw := values.Get("page"); raw != "" {
			return errWrongPagingMode("page", pagingName(spec.Paging), "cursor")
		}
		if raw := values.Get("cursor"); raw != "" {
			c, err := DecodeCursor(raw)
			if err != nil {
				return errBadCursor(raw)
			}
			q.Cursor = c
		}
	default:
		if raw := values.Get("cursor"); raw != "" {
			return errWrongPagingMode("cursor", pagingName(spec.Paging), "page")
		}
		if raw := values.Get("page"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				return errBadPagingParam("page", raw, pagingName(spec.Paging))
			}
			q.Page = n
		}
	}
	return nil
}

func pagingName(p Paging) string {
	if p == PagingCursor {
		return "cursor"
	}
	return "1-based page"
}

// maps yields the keys of url.Values; sorting them keeps filter order deterministic
// so a generated SQL condition is stable across requests.
func maps(v url.Values) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range v {
			if !yield(k) {
				return
			}
		}
	}
}
