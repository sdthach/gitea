// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"net/http"
	"testing"

	hub_model "gitea.dev/models/hub"
	"gitea.dev/services/contexttest"

	"github.com/stretchr/testify/assert"
)

// TestRenderHubErrorUsesTheErrorsOwnCodeAndStatusWhenSet covers both cases RenderHubError
// picks between: an error with its own Code and Status wins over the caller's status and the
// "hub_error" default, while an error with neither falls back to both.
func TestRenderHubErrorUsesTheErrorsOwnCodeAndStatusWhenSet(t *testing.T) {
	ctx, resp := contexttest.MockAPIContext(t, "GET /api/v1/planning/example")
	RenderHubError(ctx, http.StatusBadRequest, &hub_model.Error{
		Code: "x", Status: http.StatusConflict,
		Message: "conflicting write", SuggestedAction: "retry",
	})
	assert.Equal(t, http.StatusConflict, resp.Code)
	assert.Contains(t, resp.Body.String(), `"code":"x"`)

	ctx, resp = contexttest.MockAPIContext(t, "GET /api/v1/planning/example")
	RenderHubError(ctx, http.StatusBadRequest, &hub_model.Error{
		Message: "plain refusal", SuggestedAction: "retry",
	})
	assert.Equal(t, http.StatusBadRequest, resp.Code)
	assert.Contains(t, resp.Body.String(), `"code":"hub_error"`)
}
