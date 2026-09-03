// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseIssueIDsOrReasonAcceptsTheLimitAndRefusesOverIt is the count boundary a real
// request of this size is too slow to exercise end to end: 500 ids accepted, the 501st tipping
// bad_issue_ids.
func TestParseIssueIDsOrReasonAcceptsTheLimitAndRefusesOverIt(t *testing.T) {
	at := make([]string, maxOrderIssueIDs)
	for i := range at {
		at[i] = strconv.Itoa(i + 1)
	}
	ids, reason, ok := parseIssueIDsOrReason(at)
	require.True(t, ok, "500 ids is accepted: %s", reason)
	assert.Len(t, ids, maxOrderIssueIDs)

	over := append(append([]string(nil), at...), strconv.Itoa(maxOrderIssueIDs+1))
	_, reason, ok = parseIssueIDsOrReason(over)
	assert.False(t, ok, "501 ids is refused")
	assert.Contains(t, reason, "over the limit")
}

// TestMissingColumnIssueIDsListsWhatGivenLeavesOut proves the incomplete_column check names
// exactly the ids already in the column that the reorder call dropped, in the column's own
// order.
func TestMissingColumnIssueIDsListsWhatGivenLeavesOut(t *testing.T) {
	assert.Equal(t, []int64{2}, missingColumnIssueIDs([]int64{1, 2, 3}, []int64{1, 3}))
	assert.Empty(t, missingColumnIssueIDs([]int64{1, 2}, []int64{2, 1}), "same set, different order, is complete")
	assert.Empty(t, missingColumnIssueIDs(nil, []int64{1}), "an empty column has nothing to be missing")
}
