// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"net/http"
	"testing"
	"time"

	hub_model "gitea.dev/models/hub"
	issues_model "gitea.dev/models/issues"
	"gitea.dev/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetIssueStartRefusesAPullRequestABadStartAndAStartAfterTheDeadline is the refusal
// table: every refusal is caught before a row would be written.
func TestSetIssueStartRefusesAPullRequestABadStartAndAStartAfterTheDeadline(t *testing.T) {
	err := SetIssueStart(t.Context(), &issues_model.Issue{ID: 1, IsPull: true}, time.Unix(1_700_000_000, 0))
	require.Error(t, err)
	var hubErr *hub_model.Error
	require.ErrorAs(t, err, &hubErr)
	assert.Equal(t, "not_an_issue", hubErr.Code)
	assert.Equal(t, http.StatusUnprocessableEntity, hubErr.Status)
	assert.NotEmpty(t, hubErr.SuggestedAction)

	err = SetIssueStart(t.Context(), &issues_model.Issue{ID: 1}, time.Unix(0, 0))
	require.ErrorAs(t, err, &hubErr)
	assert.Equal(t, "bad_start", hubErr.Code)
	assert.Equal(t, http.StatusBadRequest, hubErr.Status)

	err = SetIssueStart(t.Context(), &issues_model.Issue{ID: 1, DeadlineUnix: timeutil.TimeStamp(1_700_000_000)}, time.Unix(1_700_100_000, 0))
	require.ErrorAs(t, err, &hubErr)
	assert.Equal(t, "start_after_end", hubErr.Code)
	assert.Equal(t, http.StatusUnprocessableEntity, hubErr.Status)
}

// TestSetMilestoneStartRefusesABadStartAndAStartAfterTheDueDate mirrors the issue refusal
// table, naming the due date in the suggested action.
func TestSetMilestoneStartRefusesABadStartAndAStartAfterTheDueDate(t *testing.T) {
	err := SetMilestoneStart(t.Context(), &issues_model.Milestone{ID: 1}, time.Unix(-1, 0))
	require.Error(t, err)
	var hubErr *hub_model.Error
	require.ErrorAs(t, err, &hubErr)
	assert.Equal(t, "bad_start", hubErr.Code)
	assert.Equal(t, http.StatusBadRequest, hubErr.Status)

	err = SetMilestoneStart(t.Context(), &issues_model.Milestone{ID: 1, DeadlineUnix: timeutil.TimeStamp(1_700_000_000)}, time.Unix(1_700_100_000, 0))
	require.ErrorAs(t, err, &hubErr)
	assert.Equal(t, "start_after_end", hubErr.Code)
	assert.Equal(t, http.StatusUnprocessableEntity, hubErr.Status)
	assert.Contains(t, hubErr.SuggestedAction, "2023-11-14", "the suggested action names the due date")
}
