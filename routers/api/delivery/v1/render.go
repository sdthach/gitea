// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"
	"net/url"

	delivery "gitea.dev/models/hub"
	"gitea.dev/services/context"
	"gitea.dev/services/hub/query"
)

// apiError renders a rejection. Every rejection carries a suggested next action.
func apiError(ctx *context.APIContext, status int, code, message, suggestion string) {
	ctx.JSON(status, &query.Error{
		Status:          status,
		Code:            code,
		Message:         message,
		SuggestedAction: suggestion,
	})
}

// renderQueryError renders a grammar rejection verbatim: it already names the offender and
// lists what is accepted.
func renderQueryError(ctx *context.APIContext, err *query.Error) {
	ctx.JSON(err.Status, err)
}

// renderHubError renders a hub error, which always carries its suggested action.
func renderHubError(ctx *context.APIContext, status int, err error) {
	var hubErr *delivery.Error
	if e, ok := err.(*delivery.Error); ok {
		hubErr = e
	}
	if hubErr == nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(status, &query.Error{
		Status:          status,
		Code:            "delivery_error",
		Message:         hubErr.Message,
		SuggestedAction: hubErr.SuggestedAction,
	})
}

// parseQuery reads the grammar for an operation's resource, rendering the rejection itself
// when the request is refused.
func parseQuery(ctx *context.APIContext, spec query.Spec) (*query.Query, bool) {
	values, err := url.ParseQuery(ctx.Req.URL.RawQuery)
	if err != nil {
		apiError(ctx, http.StatusBadRequest, "malformed_query",
			"the query string could not be parsed", "Percent-encode any literal & or = inside a filter value.")
		return nil, false
	}
	q, qErr := query.Parse(values, spec)
	if qErr != nil {
		renderQueryError(ctx, qErr)
		return nil, false
	}
	return q, true
}

// parseCursorQuery reads the grammar for a cursor-paged resource and refuses a cursor that
// was issued under a different sort. Following one would skip and repeat rows, which is the
// exact failure cursor paging exists to prevent.
func parseCursorQuery(ctx *context.APIContext, spec query.Spec) (*query.Query, bool) {
	q, ok := parseQuery(ctx, spec)
	if !ok {
		return nil, false
	}
	if !q.Cursor.Matches(q.Sort) {
		apiError(ctx, http.StatusBadRequest, "cursor_sort_mismatch",
			"the cursor was issued under a different sort than this request asks for",
			"Repeat the request with the sort_by and order the cursor was issued under, or drop the cursor to start the traversal again.")
		return nil, false
	}
	return q, true
}

// renderPage writes an offset-paged response with Gitea's own headers.
func renderPage(ctx *context.APIContext, q *query.Query, total int64, payload any) {
	ctx.SetTotalCountHeader(total)
	ctx.SetLinkHeader(total, q.Limit)
	ctx.JSON(http.StatusOK, payload)
}

// NextCursorHeader carries the opaque token that continues a cursor traversal. It is a
// header rather than an envelope around the rows so a cursor-paged resource and an
// offset-paged one return the same JSON shape and one client renders both.
const NextCursorHeader = "X-Next-Cursor"

// renderCursorPage writes a cursor-paged response. It carries NO total: counting a table
// that is receiving concurrent inserts answers a question that was already stale when it
// was asked.
//
// sortValue and lastID are the last row's sort value and primary key, which is what makes
// the traversal return each row exactly once while rows are being appended.
func renderCursorPage(ctx *context.APIContext, q *query.Query, rowCount int, sortValue any, lastID int64, payload any) {
	if rowCount > 0 && rowCount == q.Limit {
		next := query.NewCursor(q.Sort, sortValue, lastID).Encode()
		if next != "" {
			ctx.Resp.Header().Set(NextCursorHeader, next)
			values := ctx.Req.URL.Query()
			values.Set("cursor", next)
			ctx.Resp.Header().Set("Link", "<"+ctx.Req.URL.Path+"?"+values.Encode()+`>; rel="next"`)
		}
	}
	ctx.JSON(http.StatusOK, payload)
}

// equalityFilter reads a bare `field=value` out of a parsed query. The grid is a projection
// rather than a table, so its filters select what to project instead of rendering into a
// SQL condition; they still go through the one grammar, so an unknown field is rejected by
// the same parser every other resource uses.
func equalityFilter(q *query.Query, name string) (any, bool) {
	for _, f := range q.Filters {
		if f.Field.Name == name && f.Op == query.OpEq && len(f.Values) == 1 {
			return f.Values[0], true
		}
	}
	return nil, false
}

func equalityFilterString(q *query.Query, name string) string {
	if v, ok := equalityFilter(q, name); ok {
		if s, isString := v.(string); isString {
			return s
		}
	}
	return ""
}

func equalityFilterInt(q *query.Query, name string) int64 {
	if v, ok := equalityFilter(q, name); ok {
		if n, isInt := v.(int64); isInt {
			return n
		}
	}
	return 0
}
