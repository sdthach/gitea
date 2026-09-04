// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestCurrentISOWeekSpansMondayToSunday covers both ends of the week: a Wednesday resolves to
// its own week's Monday and Sunday, and a Sunday resolves to the Monday that started it rather
// than the next one.
func TestCurrentISOWeekSpansMondayToSunday(t *testing.T) {
	wednesday := time.Date(2026, 3, 4, 15, 30, 0, 0, time.UTC)
	from, to := CurrentISOWeek(wednesday)
	assert.Equal(t, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), from)
	assert.Equal(t, time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), to)

	sunday := time.Date(2026, 3, 8, 23, 0, 0, 0, time.UTC)
	from, to = CurrentISOWeek(sunday)
	assert.Equal(t, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), from)
	assert.Equal(t, time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), to)
}

// TestTruncateBoundary is mutation proof (e)'s pure counterpart: exactly max items is kept and
// not flagged; one more is trimmed to max and flagged. Widening the cap or dropping the check
// turns this red without needing a database seeded past the limit.
func TestTruncateBoundary(t *testing.T) {
	kept, truncated := Truncate([]int{1, 2, 3}, 3)
	assert.Len(t, kept, 3)
	assert.False(t, truncated, "exactly the cap is not truncated")

	kept, truncated = Truncate([]int{1, 2, 3, 4}, 3)
	assert.Equal(t, []int{1, 2, 3}, kept)
	assert.True(t, truncated, "one over the cap is trimmed and flagged")
}
