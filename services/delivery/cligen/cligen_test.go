// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cligen

import (
	"net/http"
	"testing"

	deliveryv1 "gitea.dev/routers/api/delivery/v1"
	"gitea.dev/services/delivery/query"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandName(t *testing.T) {
	cases := map[string]string{
		"listEnvironments":           "environments",
		"listRepoEnvironments":       "repo-environments",
		"getRepoEnvironment":         "repo-environment",
		"listRepoEnvironmentSecrets": "repo-environment-secrets",
		"listRepos":                  "repos",
		"deploy":                     "deploy",
		"listing":                    "listing",
	}
	for id, want := range cases {
		assert.Equal(t, want, CommandName(id), "operation id %q", id)
	}
}

// TestEveryOperationGetsACommand is K7 at the source: the parity check reads the published
// document, and this asserts the generator cannot produce a document without a command.
func TestEveryOperationGetsACommand(t *testing.T) {
	ops := deliveryv1.Operations()
	require.NotEmpty(t, ops)
	names := CommandNames(ops)
	assert.Len(t, names, len(ops))
	for _, name := range names {
		assert.NotEmpty(t, name)
	}
}

func TestRenderClientIsDeterministic(t *testing.T) {
	ops := deliveryv1.Operations()
	first, err := RenderClient(ops)
	require.NoError(t, err)
	second, err := RenderClient(ops)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second))
	assert.Contains(t, string(first), "DO NOT EDIT")
	assert.Contains(t, string(first), "var Commands = []Command{")
}

// TestRenderClientRefusesCollidingCommands proves the generator fails rather than silently
// dropping one of two endpoints that map to the same command name.
func TestRenderClientRefusesCollidingCommands(t *testing.T) {
	spec := query.Spec{Resource: "x", PrimaryKey: "id", Paging: query.PagingOffset}
	_, err := RenderClient([]*deliveryv1.Operation{
		{ID: "listThings", Method: http.MethodGet, Path: "/things", Summary: "a", Response: "Environment", Query: &spec},
		{ID: "getThings", Method: http.MethodGet, Path: "/things/x", Summary: "b", Response: "Environment", Query: &spec},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both map to command")
}
