// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package query

import (
	"encoding/base64"
	"errors"
	"fmt"

	"gitea.dev/modules/json"
)

// cursorVersion is stamped into every token so a token issued by an older shape is
// rejected rather than silently misread.
const cursorVersion = 1

// Cursor is the opaque position of a cursor-paged traversal (I6). It carries the last
// row's sort value and primary key, so paging while rows are being written returns each
// row exactly once: an offset traversal over the same table duplicates and skips.
type Cursor struct {
	Version int    `json:"v"`
	Column  string `json:"c"`
	Order   string `json:"o"`
	Value   any    `json:"s"`
	ID      int64  `json:"k"`
}

// IsZero reports whether the traversal starts at the beginning.
func (c Cursor) IsZero() bool { return c.Version == 0 }

// ErrBadCursor is returned by DecodeCursor for a token this endpoint did not issue.
var ErrBadCursor = errors.New("cursor is not a token this endpoint issued")

// NewCursor builds the token that points just past the given row.
func NewCursor(sort Sort, sortValue any, id int64) Cursor {
	return Cursor{Version: cursorVersion, Column: sort.Column, Order: sort.Order, Value: sortValue, ID: id}
}

// Encode renders the cursor as an opaque token.
func (c Cursor) Encode() string {
	if c.IsZero() {
		return ""
	}
	raw, err := json.Marshal(c)
	if err != nil {
		// Cursor holds only JSON-representable scalars, so this cannot happen for a
		// cursor NewCursor built; returning "" ends the traversal rather than looping.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCursor reads a token back. A token that does not decode is an error, never an
// ignored parameter: silently restarting a traversal repeats rows the caller already saw.
func DecodeCursor(token string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: %v", ErrBadCursor, err)
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cursor{}, fmt.Errorf("%w: %v", ErrBadCursor, err)
	}
	if c.Version != cursorVersion || c.Column == "" || (c.Order != OrderAsc && c.Order != OrderDesc) {
		return Cursor{}, ErrBadCursor
	}
	return c, nil
}

// Matches reports whether the cursor was issued for this sort. A cursor followed under a
// different sort would skip and repeat rows, so a mismatch is refused.
func (c Cursor) Matches(sort Sort) bool {
	return c.IsZero() || (c.Column == sort.Column && c.Order == sort.Order)
}
