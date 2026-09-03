// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cligen

import (
	"net/http"
	"testing"

	hubapi "gitea.dev/routers/api/hub"
	planningv1 "gitea.dev/routers/api/planning/v1"
	"gitea.dev/services/hub/query"

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

// TestEveryOperationGetsACommand works at the source: the parity check reads the published
// document, and this asserts the generator cannot produce a document without a command. Any
// area's operation set proves the property; planning is used here because it is the smaller.
func TestEveryOperationGetsACommand(t *testing.T) {
	ops := planningv1.Operations()
	require.NotEmpty(t, ops)
	names := CommandNames(ops)
	// An operation may serve more than one command — deploy and rollback compose
	// the identical request — so the count is a lower bound, not an equality. What has to
	// hold is that every operation yields at least one command and that no two operations
	// answer to the same name, which the parity check would otherwise discover in the
	// published document rather than here.
	assert.GreaterOrEqual(t, len(names), len(ops), "every operation yields at least one command")
	seen := map[string]bool{}
	for _, op := range ops {
		served := CommandNamesFor(op)
		assert.NotEmpty(t, served, "operation %q serves no command", op.ID)
		for _, name := range served {
			assert.NotEmpty(t, name)
			assert.False(t, seen[name], "command %q is served by more than one operation", name)
			seen[name] = true
		}
	}
	assert.Len(t, names, len(seen))
}

func TestRenderClientIsDeterministic(t *testing.T) {
	ops := planningv1.Operations()
	schemas := planningv1.Schemas()
	first, err := RenderClient(ops, schemas)
	require.NoError(t, err)
	second, err := RenderClient(ops, schemas)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second))
	assert.Contains(t, string(first), "DO NOT EDIT")
	assert.Contains(t, string(first), "var Commands = []hubcli.Command{")
}

// TestRenderClientRefusesCollidingCommands proves the generator fails rather than silently
// dropping one of two endpoints that map to the same command name.
func TestRenderClientRefusesCollidingCommands(t *testing.T) {
	spec := query.Spec{Resource: "x", PrimaryKey: "id", Paging: query.PagingOffset}
	_, err := RenderClient([]*hubapi.Operation{
		{ID: "listThings", Method: http.MethodGet, Path: "/things", Summary: "a", Response: "Environment", Query: &spec},
		{ID: "getThings", Method: http.MethodGet, Path: "/things/x", Summary: "b", Response: "Environment", Query: &spec},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both map to command")
}
