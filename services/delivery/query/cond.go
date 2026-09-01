// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package query

import (
	"strings"

	"xorm.io/builder"
)

// Cond renders the parsed filters and free-text search as one condition. Column names
// come from the resource's own whitelist, never from the request, so no caller input
// reaches the statement as an identifier.
func (q *Query) Cond() builder.Cond {
	cond := builder.NewCond()
	for _, f := range q.Filters {
		cond = cond.And(f.cond())
	}
	if q.Search != "" && len(q.Spec.SearchFields) > 0 {
		search := builder.NewCond()
		for _, col := range q.Spec.SearchFields {
			search = search.Or(builder.Like{col, q.Search})
		}
		cond = cond.And(search)
	}
	return cond
}

func (f Filter) cond() builder.Cond {
	col := f.Field.Column
	switch f.Op {
	case OpNe:
		return builder.Neq{col: f.Values[0]}
	case OpLt:
		return builder.Lt{col: f.Values[0]}
	case OpLte:
		return builder.Lte{col: f.Values[0]}
	case OpGt:
		return builder.Gt{col: f.Values[0]}
	case OpGte:
		return builder.Gte{col: f.Values[0]}
	case OpIn:
		return builder.In(col, f.Values...)
	case OpContains:
		return builder.Like{col, f.Text}
	}
	return builder.Eq{col: f.Values[0]}
}

// OrderBy renders the sort. Every sort is tie-broken on the primary key; without the
// tie-breaker pagination repeats and skips rows (I5).
func (q *Query) OrderBy() string {
	dir := strings.ToUpper(q.Sort.Order)
	if q.Sort.Column == q.Sort.TieBreaker {
		return q.Sort.Column + " " + dir
	}
	return q.Sort.Column + " " + dir + ", " + q.Sort.TieBreaker + " " + dir
}

// CursorCond narrows to the rows after the cursor position under the current sort.
func (q *Query) CursorCond() builder.Cond {
	if q.Cursor.IsZero() {
		return builder.NewCond()
	}
	col, pk := q.Sort.Column, q.Sort.TieBreaker
	if q.Sort.Order == OrderDesc {
		return builder.Or(
			builder.Lt{col: q.Cursor.Value},
			builder.And(builder.Eq{col: q.Cursor.Value}, builder.Lt{pk: q.Cursor.ID}),
		)
	}
	return builder.Or(
		builder.Gt{col: q.Cursor.Value},
		builder.And(builder.Eq{col: q.Cursor.Value}, builder.Gt{pk: q.Cursor.ID}),
	)
}

// Offset is the 1-based page rendered as a row offset (I7).
func (q *Query) Offset() int { return (q.Page - 1) * q.Limit }
