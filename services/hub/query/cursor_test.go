// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package query

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorRoundTrip(t *testing.T) {
	sortBy := Sort{Column: "created_unix", Order: OrderAsc, TieBreaker: "id"}
	c := NewCursor(sortBy, float64(1788220800), 42)

	decoded, err := DecodeCursor(c.Encode())
	require.NoError(t, err)
	assert.Equal(t, c, decoded)
	assert.True(t, decoded.Matches(sortBy))
	assert.False(t, decoded.Matches(Sort{Column: "created_unix", Order: OrderDesc, TieBreaker: "id"}),
		"a cursor followed under a different sort would skip and repeat rows")
}

func TestZeroCursorEncodesEmpty(t *testing.T) {
	assert.Empty(t, Cursor{}.Encode())
	assert.True(t, Cursor{}.Matches(Sort{Column: "x", Order: OrderAsc}))
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	for _, token := range []string{"not base64 !!", "eyJ2IjowfQ", "e30", "eyJ2IjoxLCJjIjoiIn0"} {
		_, err := DecodeCursor(token)
		require.ErrorIs(t, err, ErrBadCursor, "token %q must be refused, never silently restarted", token)
	}
}

func TestCursorCondIsPositional(t *testing.T) {
	q := &Query{Sort: Sort{Column: "created_unix", Order: OrderAsc, TieBreaker: "id"}}
	assert.Empty(t, condSQL(t, q.CursorCond()), "an empty cursor starts at the beginning")

	q.Cursor = NewCursor(q.Sort, int64(100), 7)
	assert.Equal(t, "created_unix>? OR (created_unix=? AND id>?)", condSQL(t, q.CursorCond()))

	q.Sort.Order = OrderDesc
	q.Cursor = NewCursor(q.Sort, int64(100), 7)
	assert.Equal(t, "created_unix<? OR (created_unix=? AND id<?)", condSQL(t, q.CursorCond()))
}

// row is a minimal append-only row: a sort value and a primary key.
type row struct {
	sortValue int64
	id        int64
}

// table is an append-only table that new rows can be written to mid-traversal.
type table struct{ rows []row }

func (tb *table) append(r row) { tb.rows = append(tb.rows, r) }

func (tb *table) ordered() []row {
	out := append([]row(nil), tb.rows...)
	// Newest first, the order an append-only log is read in, tie-broken on the primary key.
	sort.Slice(out, func(i, j int) bool {
		if out[i].sortValue != out[j].sortValue {
			return out[i].sortValue > out[j].sortValue
		}
		return out[i].id > out[j].id
	})
	return out
}

// pageByCursor reads one page after the cursor position, exactly as CursorCond narrows a
// descending traversal.
func (tb *table) pageByCursor(c Cursor, limit int) ([]row, Cursor) {
	var page []row
	for _, r := range tb.ordered() {
		if !c.IsZero() {
			after := r.sortValue < toInt64(c.Value) ||
				(r.sortValue == toInt64(c.Value) && r.id < c.ID)
			if !after {
				continue
			}
		}
		page = append(page, r)
		if len(page) == limit {
			break
		}
	}
	if len(page) == 0 {
		return nil, Cursor{}
	}
	last := page[len(page)-1]
	return page, NewCursor(Sort{Column: "sort_value", Order: OrderDesc, TieBreaker: "id"}, last.sortValue, last.id)
}

// pageByOffset reads one page by row offset, as offset paging does.
func (tb *table) pageByOffset(offset, limit int) []row {
	ordered := tb.ordered()
	if offset >= len(ordered) {
		return nil
	}
	return ordered[offset:min(offset+limit, len(ordered))]
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

// TestCursorPagingUnderConcurrentInsert: paging by cursor while
// rows are being written returns each row exactly once, and the same traversal by offset is
// shown to duplicate and skip.
//
// The log is read newest-first, so a row written during the traversal lands ahead of the
// reader and shifts every offset by one.
func TestCursorPagingUnderConcurrentInsert(t *testing.T) {
	const rowCount, limit = 6, 2
	const pages = rowCount / limit // a reader asking for exactly the rows that existed

	seed := func() *table {
		tb := &table{}
		for i := int64(1); i <= rowCount; i++ {
			tb.append(row{sortValue: i * 10, id: i})
		}
		return tb
	}
	arriving := row{sortValue: 70, id: 100} // written after the traversal started

	cursorSeen := map[int64]int{}
	tb := seed()
	var c Cursor
	for step := range pages {
		page, next := tb.pageByCursor(c, limit)
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			cursorSeen[r.id]++
		}
		if step == 0 {
			tb.append(arriving)
		}
		c = next
	}

	offsetSeen := map[int64]int{}
	tb = seed()
	for step := range pages {
		page := tb.pageByOffset(step*limit, limit)
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			offsetSeen[r.id]++
		}
		if step == 0 {
			tb.append(arriving)
		}
	}

	for id, n := range cursorSeen {
		assert.Equal(t, 1, n, "cursor traversal returned row %d %d times, expected exactly once", id, n)
	}
	assert.Len(t, cursorSeen, rowCount, "cursor traversal saw every row present when it started")
	assert.NotContains(t, cursorSeen, arriving.id, "a row written after the traversal started is not part of it")

	duplicated, skipped := 0, 0
	for id := int64(1); id <= rowCount; id++ {
		switch {
		case offsetSeen[id] > 1:
			duplicated++
		case offsetSeen[id] == 0:
			skipped++
		}
	}
	assert.Positive(t, duplicated, "offset paging must be shown to duplicate rows, otherwise the cursor form is unjustified")
	assert.Positive(t, skipped, "offset paging must be shown to skip rows")
}
