// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package hub

import (
	"testing"

	"gitea.dev/services/hub/query"

	"github.com/stretchr/testify/assert"
)

// TestGrammarParamsCoverEveryOperator makes the document the contract the CLI builds
// against: a filter the server accepts but the document omits would be undiscoverable.
func TestGrammarParamsCoverEveryOperator(t *testing.T) {
	op := &Operation{Query: &query.Spec{
		Resource:   "example",
		Fields:     []query.Field{{Name: "size", Column: "size", Kind: query.KindInt}},
		SortFields: []string{"size"},
		PrimaryKey: "id",
		Paging:     query.PagingOffset,
	}}
	names := map[string]bool{}
	for _, p := range op.GrammarParams() {
		names[p.Name] = true
		assert.NotEmpty(t, p.Description, "parameter %q must document itself", p.Name)
	}
	for _, want := range []string{"size", "size[ne]", "size[lt]", "size[lte]", "size[gt]", "size[gte]", "size[in]", "sort_by", "order", "limit", "page"} {
		assert.True(t, names[want], "the document omits %q", want)
	}
	assert.False(t, names["cursor"], "an offset-paged resource documents page, not cursor")
}

func TestCursorResourcesDocumentCursorNotPage(t *testing.T) {
	op := &Operation{Query: &query.Spec{
		Resource: "audit", Fields: []query.Field{{Name: "id", Column: "id", Kind: query.KindInt}},
		PrimaryKey: "id", Paging: query.PagingCursor,
	}}
	names := map[string]bool{}
	for _, p := range op.GrammarParams() {
		names[p.Name] = true
	}
	assert.True(t, names["cursor"])
	assert.False(t, names["page"], "an append-only resource pages by cursor only")
}
