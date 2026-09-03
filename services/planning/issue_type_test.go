// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"testing"

	hub_model "gitea.dev/models/hub"
	planning_model "gitea.dev/models/planning"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/svg"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hubCode(t *testing.T, err error) string {
	t.Helper()
	var hubErr *hub_model.Error
	require.ErrorAs(t, err, &hubErr)
	assert.NotEmpty(t, hubErr.SuggestedAction, "every refusal carries a suggested next action")
	return hubErr.Code
}

// TestPlanningValidateTypeNameLowerCasesAndRejectsBadShapes is the validation table: no
// database, every case a name can go wrong in one place.
func TestPlanningValidateTypeNameLowerCasesAndRejectsBadShapes(t *testing.T) {
	name, err := validateTypeName("  Bug Fix  ")
	require.NoError(t, err)
	assert.Equal(t, "bug fix", name, "stored lower-cased regardless of how it was typed")

	for _, raw := range []string{"", "   ", "-bug", " _bug", string(make([]byte, 51))} {
		_, err := validateTypeName(raw)
		require.Error(t, err, "%q", raw)
		assert.Equal(t, "bad_type_name", hubCode(t, err))
	}

	// Exactly 50 characters is still accepted; 51 is not.
	fifty := ""
	for range 50 {
		fifty += "a"
	}
	_, err = validateTypeName(fifty)
	require.NoError(t, err)
}

// TestPlanningValidateIconChecksTheSvgRegistry measures svg.RenderHTML's own behaviour: it
// answers an unknown icon with a `<span>` placeholder rather than an error, so "starts with
// <svg" is what bad_icon actually checks.
func TestPlanningValidateIconChecksTheSvgRegistry(t *testing.T) {
	restore := svg.MockIcon("octicon-issue-opened")
	defer restore()

	require.NoError(t, validateIcon("octicon-issue-opened"))

	err := validateIcon("not-a-real-icon")
	require.Error(t, err)
	assert.Equal(t, "bad_icon", hubCode(t, err))
}

func TestPlanningValidateRankRejectsOutOfRange(t *testing.T) {
	require.NoError(t, validateRank(1))
	require.NoError(t, validateRank(9))

	for _, rank := range []int{0, -1, 10} {
		err := validateRank(rank)
		require.Error(t, err, rank)
		assert.Equal(t, "bad_rank", hubCode(t, err))
	}
}

// TestPlanningValidateScopeRefusesBothOrNeitherWithoutSiteAdmin covers bad_scope: repo and org
// both set, and neither set from a caller who is not a site administrator. Neither set from a
// site administrator is the instance scope and is allowed.
func TestPlanningValidateScopeRefusesBothOrNeitherWithoutSiteAdmin(t *testing.T) {
	admin := &user_model.User{IsAdmin: true}
	nonAdmin := &user_model.User{IsAdmin: false}

	err := validateScope(Scope{RepoID: 1, OrgID: 1}, admin, "type")
	require.Error(t, err)
	assert.Equal(t, "bad_scope", hubCode(t, err))

	err = validateScope(Scope{}, nonAdmin, "type")
	require.Error(t, err)
	assert.Equal(t, "bad_scope", hubCode(t, err))

	assert.NoError(t, validateScope(Scope{}, admin, "type"), "a site administrator may scope to the instance")
	assert.NoError(t, validateScope(Scope{RepoID: 1}, nonAdmin, "type"), "scope validity does not itself require site admin")
}

// TestPlanningShadowTypesPrefersTheNearestScope is what makes TypesFor a merge rather than a
// concatenation: a name declared at two scopes is published once, from the nearer one.
func TestPlanningShadowTypesPrefersTheNearestScope(t *testing.T) {
	rows := []*planning_model.IssueType{
		{ID: 1, Name: "bug", Rank: 3},            // instance
		{ID: 2, Name: "bug", OrgID: 5, Rank: 3},  // org
		{ID: 3, Name: "bug", RepoID: 7, Rank: 3}, // repo
		{ID: 4, Name: "epic", Rank: 1},           // instance only
	}
	out := shadowTypes(rows, 7, 5)
	byName := map[string]VisibleType{}
	for _, v := range out {
		byName[v.Name] = v
	}
	require.Contains(t, byName, "bug")
	assert.EqualValues(t, 3, byName["bug"].ID, "the repo-scoped row wins over org and instance")
	assert.Equal(t, ScopeRepo, byName["bug"].Scope)
	assert.EqualValues(t, 7, byName["bug"].ScopeID)

	require.Contains(t, byName, "epic")
	assert.Equal(t, ScopeInstance, byName["epic"].Scope, "no narrower row exists for epic")

	// With no repo or org given, only the instance row is a candidate for "bug".
	out = shadowTypes(rows, 0, 0)
	byName = map[string]VisibleType{}
	for _, v := range out {
		byName[v.Name] = v
	}
	assert.EqualValues(t, 1, byName["bug"].ID)
	assert.Equal(t, ScopeInstance, byName["bug"].Scope)
}
