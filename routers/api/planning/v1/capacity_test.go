// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"testing"

	issues_model "gitea.dev/models/issues"

	"github.com/stretchr/testify/assert"
)

// fakeIssues builds a fake issue list of length n, cheap enough to cover the truncation
// boundary without seeding a database with 2001 real rows.
func fakeIssues(n int) issues_model.IssueList {
	out := make(issues_model.IssueList, n)
	for i := range out {
		out[i] = &issues_model.Issue{}
	}
	return out
}

// TestTruncateIssuesBoundary is mutation proof for item 4: exactly maxCapacityIssues is kept
// and not truncated; one more is trimmed to the cap and flagged.
func TestTruncateIssuesBoundary(t *testing.T) {
	kept, truncated := truncateIssues(fakeIssues(maxCapacityIssues), maxCapacityIssues)
	assert.Len(t, kept, maxCapacityIssues)
	assert.False(t, truncated, "exactly the cap is not truncated")

	kept, truncated = truncateIssues(fakeIssues(maxCapacityIssues+1), maxCapacityIssues)
	assert.Len(t, kept, maxCapacityIssues)
	assert.True(t, truncated, "one over the cap is trimmed and flagged")
}
