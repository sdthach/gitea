// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseInsightsDeploymentsFieldsEmpty(t *testing.T) {
	assert.Nil(t, parseInsightsDeploymentsFields(""))
}

func TestParseInsightsDeploymentsFieldsSingle(t *testing.T) {
	got := parseInsightsDeploymentsFields("sha")
	assert.True(t, got["sha"])
	assert.False(t, got["run"])
}

func TestParseInsightsDeploymentsFieldsMultiple(t *testing.T) {
	got := parseInsightsDeploymentsFields("sha,run,duration")
	assert.True(t, got["sha"])
	assert.True(t, got["run"])
	assert.True(t, got["duration"])
	assert.False(t, got["approved_by"])
}

func TestParseInsightsDeploymentsFieldsIgnoresUnknown(t *testing.T) {
	got := parseInsightsDeploymentsFields("sha,bogus,run")
	assert.True(t, got["sha"])
	assert.True(t, got["run"])
	assert.False(t, got["bogus"])
}

func TestParseInsightsDeploymentsFieldsTrimsWhitespace(t *testing.T) {
	got := parseInsightsDeploymentsFields(" sha , run ")
	assert.True(t, got["sha"])
	assert.True(t, got["run"])
}

func TestInsightsDeploymentsSpecHasNoPagingOffset(t *testing.T) {
	assert.NotEmpty(t, insightsDeploymentsSpec.PrimaryKey, "cursor paging needs a tie-breaker")
}
