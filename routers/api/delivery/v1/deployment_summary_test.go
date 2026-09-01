// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseSummaryFieldsEmpty(t *testing.T) {
	assert.Nil(t, parseSummaryFields(""))
}

func TestParseSummaryFieldsSingle(t *testing.T) {
	got := parseSummaryFields("sha")
	assert.True(t, got["sha"])
	assert.False(t, got["run"])
}

func TestParseSummaryFieldsMultiple(t *testing.T) {
	got := parseSummaryFields("sha,run,duration")
	assert.True(t, got["sha"])
	assert.True(t, got["run"])
	assert.True(t, got["duration"])
	assert.False(t, got["approved_by"])
}

func TestParseSummaryFieldsIgnoresUnknown(t *testing.T) {
	got := parseSummaryFields("sha,bogus,run")
	assert.True(t, got["sha"])
	assert.True(t, got["run"])
	assert.False(t, got["bogus"])
}

func TestParseSummaryFieldsTrimsWhitespace(t *testing.T) {
	got := parseSummaryFields(" sha , run ")
	assert.True(t, got["sha"])
	assert.True(t, got["run"])
}

func TestDeploymentSummarySpecHasNoPagingOffset(t *testing.T) {
	assert.NotEmpty(t, deploymentSummarySpec.PrimaryKey, "cursor paging needs a tie-breaker")
}
