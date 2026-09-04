// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"bytes"
	"strings"
	"testing"

	"gitea.dev/modules/json"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEveryEndpointIsDocumentedAndHandled works at the source: Routes mounts the
// operation list, so an endpoint that is not documented cannot be served, and a documented
// operation with no handler is a defect rather than a 404 to discover in production.
func TestEveryEndpointIsDocumentedAndHandled(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range endpoints() {
		require.NotNil(t, e.Op, "an endpoint without an Operation cannot be documented")
		require.NotNil(t, e.Handler, "operation %q has no handler", e.Op.ID)
		assert.NotEmpty(t, e.Op.ID)
		assert.NotEmpty(t, e.Op.Method)
		assert.NotEmpty(t, e.Op.Path)
		assert.NotEmpty(t, e.Op.Summary, "operation %q must summarise itself; the summary is the CLI's help text", e.Op.ID)
		assert.NotEmpty(t, e.Op.Description)
		assert.NotEmpty(t, e.Op.Tag)
		assert.NotEmpty(t, e.Op.Response, "operation %q must name a published schema", e.Op.ID)
		assert.Contains(t, Schemas(), e.Op.Response, "operation %q names schema %q, which the document does not publish", e.Op.ID, e.Op.Response)
		assert.False(t, seen[e.Op.ID], "operation id %q is used twice", e.Op.ID)
		seen[e.Op.ID] = true
	}
	assert.NotEmpty(t, seen)
}

// TestEveryListSpecIsTieBroken: without a primary key to tie-break on, pagination
// repeats and skips rows.
func TestEveryListSpecIsTieBroken(t *testing.T) {
	for _, op := range Operations() {
		if op.Query == nil {
			continue
		}
		assert.NotEmpty(t, op.Query.PrimaryKey, "list operation %q declares no primary key to tie-break on", op.ID)
		for _, name := range op.Query.SortFields {
			found := false
			for _, f := range op.Query.Fields {
				if f.Name == name {
					found = true
				}
			}
			assert.True(t, found, "operation %q sorts by %q, which it does not declare as a field", op.ID, name)
		}
		assert.NotEmpty(t, op.Query.Resource)
	}
}

// TestPathParamsAppearInThePath catches a documented parameter the route never binds.
func TestPathParamsAppearInThePath(t *testing.T) {
	for _, op := range Operations() {
		for _, p := range op.PathParams {
			assert.Contains(t, op.Path, "{"+p.Name+"}", "operation %q documents path parameter %q that its path does not carry", op.ID, p.Name)
			assert.True(t, p.Required, "a path parameter is always required")
		}
	}
}

// TestOpenAPIIsDeterministic is what makes the diff gate meaningful: a generator
// whose output moved between runs would fail CI for no reason and be turned off.
func TestOpenAPIIsDeterministic(t *testing.T) {
	first, err := OpenAPI()
	require.NoError(t, err)
	second, err := OpenAPI()
	require.NoError(t, err)
	assert.True(t, bytes.Equal(first, second), "regenerating an unchanged registry must be byte-identical")
}

func TestOpenAPIDeclaresEveryOperation(t *testing.T) {
	raw, err := OpenAPI()
	require.NoError(t, err)

	var doc struct {
		OpenAPI string `json:"openapi"`
		Paths   map[string]map[string]struct {
			OperationID string           `json:"operationId"`
			Parameters  []map[string]any `json:"parameters"`
			Responses   map[string]any   `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.Equal(t, "3.0.3", doc.OpenAPI)

	documented := map[string]bool{}
	for _, methods := range doc.Paths {
		for _, op := range methods {
			documented[op.OperationID] = true
			assert.Contains(t, op.Responses, "400", "every endpoint documents its rejection")
			assert.Contains(t, op.Responses, "403", "every endpoint documents Gitea's own permission refusal")
		}
	}
	for _, op := range Operations() {
		assert.True(t, documented[op.ID], "operation %q is served but absent from the document", op.ID)
	}
	assert.Contains(t, doc.Components.Schemas, "Error")
}

// TestSecretSchemaHasNoValueField: a secret value is never readable over any
// endpoint at any scope, so there is no field to forget to strip.
func TestSecretSchemaHasNoValueField(t *testing.T) {
	schema, ok := Schemas()["SecretName"]
	require.True(t, ok)
	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	for _, forbidden := range []string{"value", "data", "secret", "plaintext"} {
		assert.NotContains(t, props, forbidden, "the secret schema must expose no value field")
	}

	raw, err := OpenAPI()
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"value"`, "no published schema exposes a secret value")
}

// TestMethodSwitchCoversAllEndpoints verifies that every endpoint's method is one the
// shared Routes builder handles. Without this, a new method would reach the default branch
// and call log.Fatal — a crash discovered in production rather than in CI.
func TestMethodSwitchCoversAllEndpoints(t *testing.T) {
	supported := map[string]bool{
		"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
	}
	for _, e := range endpoints() {
		m := strings.ToUpper(e.Op.Method)
		assert.True(t, supported[m],
			"operation %q uses method %q which the switch does not handle", e.Op.ID, m)
	}
}

// TestReadEndpointsDeclareNoBody catches a read endpoint that documents a request body,
// which no client would send and no handler here reads.
func TestReadEndpointsDeclareNoBody(t *testing.T) {
	for _, e := range endpoints() {
		if strings.EqualFold(e.Op.Method, "GET") {
			assert.Empty(t, e.Op.Body, "operation %q is GET but declares a body", e.Op.ID)
		}
	}
}
