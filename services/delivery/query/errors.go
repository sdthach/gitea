// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package query

import (
	"fmt"
	"net/http"
	"strings"
)

// Error is a rejected request. Every Error carries a suggested next action (A21):
// a message that only states what went wrong is incomplete.
type Error struct {
	Status          int      `json:"-"`
	Code            string   `json:"code"`
	Message         string   `json:"message"`
	SuggestedAction string   `json:"suggested_action"`
	Parameter       string   `json:"parameter,omitempty"`
	Accepted        []string `json:"accepted,omitempty"`
}

func (e *Error) Error() string { return e.Message }

func newError(code, parameter, message string, accepted []string, suggestion string) *Error {
	return &Error{
		Status:          http.StatusBadRequest,
		Code:            code,
		Parameter:       parameter,
		Message:         message,
		Accepted:        accepted,
		SuggestedAction: suggestion,
	}
}

func errUnknownField(resource, field string, accepted []string) *Error {
	return newError("unknown_filter_field", field,
		fmt.Sprintf("%q is not a filterable field of %q", field, resource),
		accepted,
		fmt.Sprintf("Remove %q, or use one of the accepted fields: %s", field, strings.Join(accepted, ", ")))
}

func errUnknownOperator(field, op string, accepted []string) *Error {
	return newError("unknown_filter_operator", field+"["+op+"]",
		fmt.Sprintf("%q is not a known filter operator", op),
		accepted,
		fmt.Sprintf("Rewrite the parameter as %s[%s]=..., using one of the accepted operators.", field, accepted[0]))
}

func errOperatorNotAllowed(field, op string, accepted []string) *Error {
	return newError("operator_not_allowed", field+"["+op+"]",
		fmt.Sprintf("operator %q is not allowed on field %q", op, field),
		accepted,
		fmt.Sprintf("Use one of the operators %q accepts: %s", field, strings.Join(accepted, ", ")))
}

func errUnparseableValue(field, op, value, want string) *Error {
	return newError("unparseable_filter_value", field+"["+op+"]",
		fmt.Sprintf("value %q of filter %q is not a valid %s", value, field, want),
		nil,
		fmt.Sprintf("Supply a %s for %q, for example %s.", want, field, exampleFor(want)))
}

func exampleFor(want string) string {
	switch want {
	case kindNameInt:
		return "42"
	case kindNameBool:
		return "true"
	case kindNameTime:
		return "2026-09-01T00:00:00Z or a unix timestamp such as 1788220800"
	}
	return `"text"`
}

func errUnknownSortField(resource, field string, accepted []string) *Error {
	return newError("unknown_sort_field", "sort_by",
		fmt.Sprintf("%q is not a sortable field of %q", field, resource),
		accepted,
		"Drop sort_by to use the default order, or sort by one of: "+strings.Join(accepted, ", "))
}

func errUnknownOrder(order string) *Error {
	return newError("unknown_sort_order", "order",
		fmt.Sprintf("%q is not a sort order", order),
		[]string{"asc", "desc"},
		`Use order=asc or order=desc.`)
}

func errUnknownExpand(resource, name string, accepted []string) *Error {
	return newError("unknown_expand", "expand",
		fmt.Sprintf("%q is not an expandable sub-resource of %q", name, resource),
		accepted,
		fmt.Sprintf("Remove %q from expand, or expand one of: %s", name, strings.Join(accepted, ", ")))
}

func errTooManyExpands(n, maxExpands int) *Error {
	return newError("too_many_expands", "expand",
		fmt.Sprintf("%d expansions requested, the maximum is %d", n, maxExpands),
		nil,
		fmt.Sprintf("Request at most %d sub-resources in one call and fetch the rest separately.", maxExpands))
}

func errNestedExpand(name string) *Error {
	return newError("nested_expand", "expand",
		fmt.Sprintf("%q asks for a nested expansion; expansion is one level deep", name),
		nil,
		"Expand the sub-resource directly, then fetch its own sub-resources from their own endpoint.")
}

func errBadPagingParam(param, value, mode string) *Error {
	return newError("bad_paging_parameter", param,
		fmt.Sprintf("%q is not a valid %s", value, param),
		nil,
		fmt.Sprintf("This resource pages by %s. Supply a positive integer for %q.", mode, param))
}

func errWrongPagingMode(param, mode, use string) *Error {
	return newError("wrong_paging_mode", param,
		fmt.Sprintf("%q is not accepted: this resource pages by %s", param, mode),
		[]string{use},
		fmt.Sprintf("Page this resource with %q instead of %q.", use, param))
}

func errBadCursor(value string) *Error {
	return newError("bad_cursor", "cursor",
		fmt.Sprintf("cursor %q is not a cursor this endpoint issued", value),
		nil,
		`Start the traversal again without "cursor" and follow the "next" token each response returns.`)
}
