// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package query implements the one query grammar every /api/delivery/v1 list endpoint
// answers (I2-I10). A resource declares its own whitelists in a Spec; it never restates
// the grammar.
package query

import "slices"

// Op is a filter operator (I3).
type Op string

const (
	OpEq       Op = "eq"
	OpNe       Op = "ne"
	OpLt       Op = "lt"
	OpLte      Op = "lte"
	OpGt       Op = "gt"
	OpGte      Op = "gte"
	OpIn       Op = "in"
	OpContains Op = "contains"
)

// AllOps is the complete operator set, in the order I3 lists it.
var AllOps = []Op{OpEq, OpNe, OpLt, OpLte, OpGt, OpGte, OpIn, OpContains}

func opNames(ops []Op) []string {
	names := make([]string, len(ops))
	for i, op := range ops {
		names[i] = string(op)
	}
	return names
}

// Kind is the value type of a filterable field. Only the types M3 allows appear here.
type Kind int

const (
	KindString Kind = iota
	KindInt
	KindBool
	KindTime
)

const (
	kindNameString = "string"
	kindNameInt    = "integer"
	kindNameBool   = "boolean"
	kindNameTime   = "timestamp"
)

func (k Kind) String() string {
	switch k {
	case KindInt:
		return kindNameInt
	case KindBool:
		return kindNameBool
	case KindTime:
		return kindNameTime
	}
	return kindNameString
}

// DefaultOps are the operators a field accepts when it names none of its own.
func (k Kind) DefaultOps() []Op {
	switch k {
	case KindBool:
		return []Op{OpEq, OpNe}
	case KindString:
		return []Op{OpEq, OpNe, OpIn, OpContains}
	}
	return []Op{OpEq, OpNe, OpLt, OpLte, OpGt, OpGte, OpIn}
}

// Field is one filterable field of a resource.
type Field struct {
	Name   string // the name callers use
	Column string // the database column it maps to
	Kind   Kind
	Ops    []Op // nil means Kind.DefaultOps()
}

func (f Field) ops() []Op {
	if len(f.Ops) > 0 {
		return f.Ops
	}
	return f.Kind.DefaultOps()
}

func (f Field) allows(op Op) bool { return slices.Contains(f.ops(), op) }

// Paging is the pagination form a resource uses. Cursor and Offset are the only
// two forms; inventing a third breaks every existing Gitea client (I8).
type Paging int

const (
	// PagingOffset is 1-based page + limit, with X-Total-Count and RFC 5988 Link (I7).
	PagingOffset Paging = iota
	// PagingCursor is limit + opaque cursor, for append-only resources only (I6).
	PagingCursor
)

// Limits shared by every resource (I7).
const (
	DefaultLimit = 50
	MaxLimit     = 200
	MaxExpands   = 3
)

// Spec is a resource's whitelist declaration.
type Spec struct {
	Resource     string
	Fields       []Field  // filterable fields
	SortFields   []string // sortable field names; must be a subset of Fields
	DefaultSort  string   // natural order; empty means PrimaryKey
	DefaultOrder string   // "asc" or "desc"; empty means "asc"
	// PrimaryKey is the column every sort is tie-broken on. Without it pagination
	// repeats and skips rows (I5), so Parse refuses a Spec that omits it.
	PrimaryKey   string
	SearchFields []string // columns ?q= searches (I10)
	Expands      []string // whitelisted sub-resources (I9)
	Paging       Paging
}

func (s Spec) field(name string) (Field, bool) {
	for _, f := range s.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

func (s Spec) fieldNames() []string {
	names := make([]string, len(s.Fields))
	for i, f := range s.Fields {
		names[i] = f.Name
	}
	return names
}

func (s Spec) defaultSort() string {
	if s.DefaultSort != "" {
		return s.DefaultSort
	}
	return s.PrimaryKey
}

func (s Spec) defaultOrder() string {
	if s.DefaultOrder != "" {
		return s.DefaultOrder
	}
	return OrderAsc
}
