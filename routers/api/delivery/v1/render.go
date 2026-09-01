// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"net/http"
	"net/url"

	"gitea.dev/models/delivery"
	"gitea.dev/services/context"
	"gitea.dev/services/delivery/query"
)

// apiError renders a rejection. Every rejection carries a suggested next action (A21).
func apiError(ctx *context.APIContext, status int, code, message, suggestion string) {
	ctx.JSON(status, &query.Error{
		Status:          status,
		Code:            code,
		Message:         message,
		SuggestedAction: suggestion,
	})
}

// renderQueryError renders a grammar rejection verbatim: it already names the offender and
// lists what is accepted (I4).
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

// renderPage writes an offset-paged response with Gitea's own headers (I7).
func renderPage(ctx *context.APIContext, q *query.Query, total int64, payload any) {
	ctx.SetTotalCountHeader(total)
	ctx.SetLinkHeader(total, q.Limit)
	ctx.JSON(http.StatusOK, payload)
}
