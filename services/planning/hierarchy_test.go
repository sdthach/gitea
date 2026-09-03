// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"net/http"
	"testing"
	"time"

	hub_model "gitea.dev/models/hub"
	issues_model "gitea.dev/models/issues"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRankAllowsRequiresALowerParentRankNumber(t *testing.T) {
	assert.True(t, RankAllows(1, 3), "rank 1 outranks rank 3")
	assert.False(t, RankAllows(3, 3), "equal rank does not outrank")
	assert.False(t, RankAllows(4, 1), "a higher rank number does not outrank a lower one")
}

// TestDepthsWalksAChainFromItsRoot: 1 is the (unrecorded) root, 2 is its child, 3 and 4 run
// deeper still.
func TestDepthsWalksAChainFromItsRoot(t *testing.T) {
	parents := map[int64]int64{2: 1, 3: 2, 4: 3}
	depth := Depths(parents)
	assert.Equal(t, 1, depth[2])
	assert.Equal(t, 2, depth[3])
	assert.Equal(t, 3, depth[4])
}

// TestSubtreeStopsOnACycleInTheMap: a cycle a stored row should never produce — SetIssueParent
// refuses to create one — but Subtree must not hang if one is present anyway.
func TestSubtreeStopsOnACycleInTheMap(t *testing.T) {
	parents := map[int64]int64{2: 1, 1: 2, 3: 2}
	done := make(chan []int64, 1)
	go func() { done <- Subtree(parents, 1) }()
	select {
	case out := <-done:
		assert.ElementsMatch(t, []int64{2, 3}, out, "1's descendants through the cycle, each visited once")
	case <-time.After(2 * time.Second):
		t.Fatal("Subtree hung on a cycle")
	}
}

func TestRootOfWalksToTheTop(t *testing.T) {
	parents := map[int64]int64{2: 1, 3: 2, 4: 3}
	assert.EqualValues(t, 1, RootOf(parents, 4))
	assert.EqualValues(t, 1, RootOf(parents, 1), "a root is its own root")
	assert.EqualValues(t, 9, RootOf(parents, 9), "an id absent from the map is its own root")
}

func hierarchyHubCode(t *testing.T, err error) *hub_model.Error {
	t.Helper()
	var hubErr *hub_model.Error
	require.ErrorAs(t, err, &hubErr)
	assert.NotEmpty(t, hubErr.SuggestedAction, "every refusal carries a suggested next action")
	return hubErr
}

// TestSetIssueParentRefusesTheSameIssueACrossRepoPairAndAPullRequest is the refusal table's
// database-free rows: each is caught before SetIssueParent ever reads a type or a parent map.
func TestSetIssueParentRefusesTheSameIssueACrossRepoPairAndAPullRequest(t *testing.T) {
	issue := &issues_model.Issue{ID: 1, RepoID: 1}
	err := SetIssueParent(t.Context(), issue, issue)
	assert.Equal(t, "same_issue", hierarchyHubCode(t, err).Code)

	err = SetIssueParent(t.Context(), &issues_model.Issue{ID: 1, RepoID: 1}, &issues_model.Issue{ID: 2, RepoID: 2})
	hubErr := hierarchyHubCode(t, err)
	assert.Equal(t, "cross_repo", hubErr.Code)
	assert.Equal(t, http.StatusUnprocessableEntity, hubErr.Status)

	err = SetIssueParent(t.Context(), &issues_model.Issue{ID: 1, RepoID: 1, IsPull: true}, &issues_model.Issue{ID: 2, RepoID: 1})
	assert.Equal(t, "pull_request", hierarchyHubCode(t, err).Code)

	err = SetIssueParent(t.Context(), &issues_model.Issue{ID: 1, RepoID: 1}, &issues_model.Issue{ID: 2, RepoID: 1, IsPull: true})
	assert.Equal(t, "pull_request", hierarchyHubCode(t, err).Code)
}
