// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package query

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xorm.io/builder"
)

func testSpec() Spec {
	return Spec{
		Resource: "deployments",
		Fields: []Field{
			{Name: "id", Column: "id", Kind: KindInt},
			{Name: "environment", Column: "environment", Kind: KindString},
			{Name: "release_tag", Column: "release_tag", Kind: KindString},
			{Name: "created_at", Column: "created_unix", Kind: KindTime},
			{Name: "is_current", Column: "is_current", Kind: KindBool},
		},
		SortFields:   []string{"id", "created_at", "environment"},
		DefaultSort:  "created_at",
		DefaultOrder: OrderDesc,
		PrimaryKey:   "id",
		SearchFields: []string{"release_tag", "environment"},
		Expands:      []string{"release", "audit", "approval"},
		Paging:       PagingOffset,
	}
}

func parse(t *testing.T, raw string) (*Query, *Error) {
	t.Helper()
	values, err := url.ParseQuery(raw)
	require.NoError(t, err)
	return Parse(values, testSpec())
}

// TestParseEveryOperator covers each operator in I3, including the bare form meaning eq.
func TestParseEveryOperator(t *testing.T) {
	cases := []struct {
		raw       string
		wantOp    Op
		wantValue any
	}{
		{"id=7", OpEq, int64(7)},
		{"id[eq]=7", OpEq, int64(7)},
		{"id[ne]=7", OpNe, int64(7)},
		{"id[lt]=7", OpLt, int64(7)},
		{"id[lte]=7", OpLte, int64(7)},
		{"id[gt]=7", OpGt, int64(7)},
		{"id[gte]=7", OpGte, int64(7)},
		{"environment[contains]=pro", OpContains, "pro"},
		{"is_current=true", OpEq, true},
		{"created_at[gte]=1788220800", OpGte, int64(1788220800)},
		{"created_at[gte]=2026-09-01T00:00:00Z", OpGte, int64(1788220800)},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			q, err := parse(t, c.raw)
			require.Nil(t, err)
			require.Len(t, q.Filters, 1)
			assert.Equal(t, c.wantOp, q.Filters[0].Op)
			assert.Equal(t, c.wantValue, q.Filters[0].Values[0])
		})
	}
}

func TestParseInOperatorSplitsValues(t *testing.T) {
	q, err := parse(t, "environment[in]=qa,prod")
	require.Nil(t, err)
	require.Len(t, q.Filters, 1)
	assert.Equal(t, OpIn, q.Filters[0].Op)
	assert.Equal(t, []any{"qa", "prod"}, q.Filters[0].Values)
}

// TestRepeatingAFieldAnds covers I3's "repeating a field ANDs the conditions".
func TestRepeatingAFieldAnds(t *testing.T) {
	q, err := parse(t, "created_at[gte]=10&created_at[gte]=20")
	require.Nil(t, err)
	assert.Len(t, q.Filters, 2)
	sql, args, buildErr := builder.ToSQL(q.Cond())
	require.NoError(t, buildErr)
	assert.Equal(t, "created_unix>=? AND created_unix>=?", sql)
	assert.Equal(t, []any{int64(10), int64(20)}, args)
}

// TestRejections covers every rejection in I4. Each must name the offender and say what is
// accepted; a filter is never silently dropped.
func TestRejections(t *testing.T) {
	cases := []struct {
		name         string
		raw          string
		wantCode     string
		wantInMsg    string
		wantAccepted bool
	}{
		{"unknown field", "colour=red", "unknown_filter_field", "colour", true},
		{"unknown operator", "id[approx]=7", "unknown_filter_operator", "approx", true},
		{"operator not allowed on kind", "is_current[gte]=true", "operator_not_allowed", "is_current", true},
		{"unparseable int", "id=seven", "unparseable_filter_value", "seven", false},
		{"unparseable bool", "is_current=maybe", "unparseable_filter_value", "maybe", false},
		{"unparseable time", "created_at[gte]=yesterday", "unparseable_filter_value", "yesterday", false},
		{"unknown sort field", "sort_by=colour", "unknown_sort_field", "colour", true},
		{"unknown order", "order=sideways", "unknown_sort_order", "sideways", true},
		{"unknown expand", "expand=repository", "unknown_expand", "repository", true},
		{"nested expand", "expand=release.author", "nested_expand", "release.author", false},
		{"bad limit", "limit=none", "bad_paging_parameter", "limit", false},
		{"zero page", "page=0", "bad_paging_parameter", "page", false},
		{"cursor on an offset resource", "cursor=abc", "wrong_paging_mode", "cursor", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q, err := parse(t, c.raw)
			require.Nil(t, q, "a refused request must return no query: an unfiltered result set is worse than an error")
			require.NotNil(t, err)
			assert.Equal(t, 400, err.Status)
			if c.wantCode != "" {
				assert.Equal(t, c.wantCode, err.Code)
			}
			if c.wantInMsg != "" {
				assert.Contains(t, err.Message, c.wantInMsg, "the message must name the offender")
			}
			assert.NotEmpty(t, err.SuggestedAction, "every error carries a suggested next action (A21)")
			if c.wantAccepted {
				assert.NotEmpty(t, err.Accepted, "the rejection must list what is accepted")
			}
		})
	}
}

func TestTooManyExpandsIsRefused(t *testing.T) {
	spec := testSpec()
	spec.Expands = []string{"a", "b", "c", "d"}
	values, parseErr := url.ParseQuery("expand=a,b,c,d")
	require.NoError(t, parseErr)
	q, err := Parse(values, spec)
	require.Nil(t, q)
	require.NotNil(t, err)
	assert.Equal(t, "too_many_expands", err.Code)
	assert.NotEmpty(t, err.SuggestedAction)
}

func TestExpandDeduplicatesAndKeepsOrder(t *testing.T) {
	q, err := parse(t, "expand=audit,release,audit")
	require.Nil(t, err)
	assert.Equal(t, []string{"audit", "release"}, q.Expand)
}

// TestSortIsAlwaysTieBroken covers I5: without the tie-breaker, pagination repeats and
// skips rows.
func TestSortIsAlwaysTieBroken(t *testing.T) {
	q, err := parse(t, "sort_by=environment&order=asc")
	require.Nil(t, err)
	assert.Equal(t, "environment ASC, id ASC", q.OrderBy())

	q, err = parse(t, "")
	require.Nil(t, err)
	assert.Equal(t, "created_unix DESC, id DESC", q.OrderBy(), "the default sort is the resource's natural order, still tie-broken")

	q, err = parse(t, "sort_by=id")
	require.Nil(t, err)
	assert.Equal(t, "id DESC", q.OrderBy(), "sorting by the primary key needs no second term")
}

func TestPagingDefaultsAndCap(t *testing.T) {
	q, err := parse(t, "")
	require.Nil(t, err)
	assert.Equal(t, DefaultLimit, q.Limit)
	assert.Equal(t, 1, q.Page)
	assert.Equal(t, 0, q.Offset())

	q, err = parse(t, "limit=1000&page=3")
	require.Nil(t, err)
	assert.Equal(t, MaxLimit, q.Limit, "limit caps at 200")
	assert.Equal(t, 3, q.Page)
	assert.Equal(t, 400, q.Offset())
}

func TestSearchComposesWithFilters(t *testing.T) {
	q, err := parse(t, "q=v1.2&environment=prod")
	require.Nil(t, err)
	sql, args, buildErr := builder.ToSQL(q.Cond())
	require.NoError(t, buildErr)
	assert.Equal(t, "environment=? AND (release_tag LIKE ? OR environment LIKE ?)", sql)
	assert.Equal(t, []any{"prod", "%v1.2%", "%v1.2%"}, args)
}

func TestPageIsRefusedOnACursorResource(t *testing.T) {
	spec := testSpec()
	spec.Paging = PagingCursor
	values, parseErr := url.ParseQuery("page=2")
	require.NoError(t, parseErr)
	q, err := Parse(values, spec)
	require.Nil(t, q)
	require.NotNil(t, err)
	assert.Equal(t, "wrong_paging_mode", err.Code)
	assert.Equal(t, []string{"cursor"}, err.Accepted)
}

func TestReservedNamesAreNotFilters(t *testing.T) {
	q, err := parse(t, "q=x&sort_by=id&order=asc&limit=10&page=2&expand=release")
	require.Nil(t, err)
	assert.Empty(t, q.Filters)
}

// TestKindNamesAppearInRejections proves Kind.String is what an unparseable-value rejection
// names, so a caller is told which type the field wanted.
func TestKindNamesAppearInRejections(t *testing.T) {
	cases := map[string]string{
		"id=seven":                 "integer",
		"is_current=maybe":         "boolean",
		"created_at[gte]=whenever": "timestamp",
	}
	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			_, err := parse(t, raw)
			require.NotNil(t, err)
			assert.Contains(t, err.Message, want, "the rejection names the type the field wanted")
			assert.Contains(t, err.SuggestedAction, want)
		})
	}
	assert.Equal(t, "string", KindString.String())
	assert.Equal(t, "integer", KindInt.String())
	assert.Equal(t, "boolean", KindBool.String())
	assert.Equal(t, "timestamp", KindTime.String())
}

// TestContainsIsRefusedOnNonTextFields: a substring match has no meaning on a number, and
// letting one through would build a LIKE against a non-text column.
func TestContainsIsRefusedOnNonTextFields(t *testing.T) {
	spec := testSpec()
	for i := range spec.Fields {
		if spec.Fields[i].Name == "id" {
			spec.Fields[i].Ops = append(spec.Fields[i].Kind.DefaultOps(), OpContains)
		}
	}
	values, parseErr := url.ParseQuery("id[contains]=7")
	require.NoError(t, parseErr)
	q, err := Parse(values, spec)
	require.Nil(t, q)
	require.NotNil(t, err)
	assert.Equal(t, "operator_not_allowed", err.Code)
	assert.NotEmpty(t, err.SuggestedAction)
}

// TestBadCursorIsRefused covers the cursor rejection: silently restarting a traversal would
// repeat rows the caller has already seen.
func TestBadCursorIsRefused(t *testing.T) {
	spec := testSpec()
	spec.Paging = PagingCursor

	values, parseErr := url.ParseQuery("cursor=not-a-cursor")
	require.NoError(t, parseErr)
	q, err := Parse(values, spec)
	require.Nil(t, q)
	require.NotNil(t, err)
	assert.Equal(t, "bad_cursor", err.Code)
	assert.Contains(t, err.Message, "not-a-cursor")
	assert.Contains(t, err.SuggestedAction, "next")

	// A well-formed cursor is accepted and carried onto the query.
	good := NewCursor(Sort{Column: "created_unix", Order: OrderDesc, TieBreaker: "id"}, int64(100), 7)
	values, parseErr = url.ParseQuery("cursor=" + good.Encode())
	require.NoError(t, parseErr)
	q, err = Parse(values, spec)
	require.Nil(t, err)
	require.NotNil(t, q)
	assert.Equal(t, int64(7), q.Cursor.ID)
	assert.True(t, q.Cursor.Matches(q.Sort))
}

// TestErrorSatisfiesTheErrorInterface pins the exported method callers use when a rejection
// is handled as a plain error.
func TestErrorSatisfiesTheErrorInterface(t *testing.T) {
	_, qErr := parse(t, "colour=red")
	require.NotNil(t, qErr)
	var asError error = qErr
	assert.Equal(t, qErr.Message, asError.Error())
	assert.Contains(t, asError.Error(), "colour")
}

// TestContainsRendersALikeOnTheRawText pins the typed Text field the contains condition is
// rendered from.
func TestContainsRendersALikeOnTheRawText(t *testing.T) {
	q, err := parse(t, "environment[contains]=pro")
	require.Nil(t, err)
	require.Len(t, q.Filters, 1)
	assert.Equal(t, "pro", q.Filters[0].Text)

	sql, args, buildErr := builder.ToSQL(q.Cond())
	require.NoError(t, buildErr)
	assert.Equal(t, "environment LIKE ?", sql)
	assert.Equal(t, []any{"%pro%"}, args)

	// A non-contains filter carries no text, so nothing can render a LIKE by accident.
	q, err = parse(t, "environment=prod")
	require.Nil(t, err)
	assert.Empty(t, q.Filters[0].Text)
}
