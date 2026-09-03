// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package planning

import (
	"strconv"
	"strings"
	"testing"
	"time"

	planning_model "gitea.dev/models/planning"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanningValidateFieldKeyAcceptsTheSlugAndRejectsTheRest(t *testing.T) {
	key, err := validateFieldKey("  points  ")
	require.NoError(t, err)
	assert.Equal(t, "points", key)

	for _, raw := range []string{"", "Points", "1points", "-points", "points!", strings.Repeat("a", 41)} {
		_, err := validateFieldKey(raw)
		require.Error(t, err, "%q", raw)
		assert.Equal(t, "bad_key", hubCode(t, err))
	}

	// Exactly 40 characters is still accepted; 41 is not.
	forty := "a" + strings.Repeat("b", 39)
	_, err = validateFieldKey(forty)
	require.NoError(t, err)
}

func TestPlanningValidateFieldLabelRejectsEmptyAndOverlong(t *testing.T) {
	label, err := validateFieldLabel("  Points  ")
	require.NoError(t, err)
	assert.Equal(t, "Points", label)

	for _, raw := range []string{"", "   ", strings.Repeat("a", 101)} {
		_, err := validateFieldLabel(raw)
		require.Error(t, err, "%q", raw)
		assert.Equal(t, "bad_label", hubCode(t, err))
	}

	hundred := strings.Repeat("a", 100)
	_, err = validateFieldLabel(hundred)
	require.NoError(t, err)
}

func TestPlanningValidateFieldKindAcceptsTheDeclaredSet(t *testing.T) {
	for _, kind := range FieldKinds {
		got, err := validateFieldKind(strings.ToUpper(kind))
		require.NoError(t, err, kind)
		assert.Equal(t, kind, got, "lower-cased regardless of how it was sent")
	}

	_, err := validateFieldKind("number")
	require.Error(t, err)
	assert.Equal(t, "bad_kind", hubCode(t, err))
}

// TestPlanningValidatePointsKindRefusesAnyKindButInt: rollups sum points as one, so the key is
// reserved for the int kind regardless of scope.
func TestPlanningValidatePointsKindRefusesAnyKindButInt(t *testing.T) {
	err := validatePointsKind("points", FieldKindText)
	require.Error(t, err)
	assert.Equal(t, "bad_kind", hubCode(t, err))

	require.NoError(t, validatePointsKind("points", FieldKindInt))
	require.NoError(t, validatePointsKind("other", FieldKindText), "the reservation only names the points key")
}

// TestPlanningValidateFieldOptionsAppliesOnlyToSelect covers every option refusal: too few, too
// many, an overlong option, a duplicate — and that a non-select kind carries no options at all
// regardless of what was sent.
func TestPlanningValidateFieldOptionsAppliesOnlyToSelect(t *testing.T) {
	options, err := validateFieldOptions(FieldKindInt, []string{"whatever"})
	require.NoError(t, err)
	assert.Nil(t, options, "options mean nothing outside select")

	_, err = validateFieldOptions(FieldKindSelect, nil)
	require.Error(t, err)
	assert.Equal(t, "options_required", hubCode(t, err))

	tooMany := make([]string, 51)
	for i := range tooMany {
		tooMany[i] = "opt" + string(rune('a'+i%26)) + strconv.Itoa(i)
	}
	_, err = validateFieldOptions(FieldKindSelect, tooMany)
	require.Error(t, err)
	assert.Equal(t, "options_required", hubCode(t, err))

	_, err = validateFieldOptions(FieldKindSelect, []string{strings.Repeat("a", 101)})
	require.Error(t, err)
	assert.Equal(t, "options_required", hubCode(t, err))

	_, err = validateFieldOptions(FieldKindSelect, []string{"low", "low"})
	require.Error(t, err)
	assert.Equal(t, "options_required", hubCode(t, err))

	// The boundary: exactly 50 distinct options, each exactly 100 characters, is accepted.
	atLimit := make([]string, 50)
	for i := range atLimit {
		suffix := strconv.Itoa(i)
		atLimit[i] = suffix + strings.Repeat("a", 100-len(suffix))
	}
	options, err = validateFieldOptions(FieldKindSelect, atLimit)
	require.NoError(t, err)
	assert.Equal(t, atLimit, options)
}

// TestPlanningShadowFieldsPrefersTheNearestScope is what makes FieldsFor a merge rather than a
// concatenation: a key declared at two scopes is published once, from the nearer one.
func TestPlanningShadowFieldsPrefersTheNearestScope(t *testing.T) {
	rows := []*planning_model.Field{
		{ID: 1, Key: "points", Kind: FieldKindInt},            // instance
		{ID: 2, Key: "points", OrgID: 5, Kind: FieldKindInt},  // org
		{ID: 3, Key: "points", RepoID: 7, Kind: FieldKindInt}, // repo
		{ID: 4, Key: "notes", Kind: FieldKindText},            // instance only
	}
	out := shadowFields(rows, 7, 5)
	byKey := map[string]VisibleField{}
	for _, v := range out {
		byKey[v.Key] = v
	}
	require.Contains(t, byKey, "points")
	assert.EqualValues(t, 3, byKey["points"].ID, "the repo-scoped row wins over org and instance")
	assert.Equal(t, ScopeRepo, byKey["points"].Scope)
	assert.EqualValues(t, 7, byKey["points"].ScopeID)

	require.Contains(t, byKey, "notes")
	assert.Equal(t, ScopeInstance, byKey["notes"].Scope, "no narrower row exists for notes")

	out = shadowFields(rows, 0, 0)
	byKey = map[string]VisibleField{}
	for _, v := range out {
		byKey[v.Key] = v
	}
	assert.EqualValues(t, 1, byKey["points"].ID)
	assert.Equal(t, ScopeInstance, byKey["points"].Scope)
}

func TestPlanningIsClearValueRecognisesNullAndBlankStringsOnly(t *testing.T) {
	assert.True(t, isClearValue(nil))
	assert.True(t, isClearValue(""))
	assert.True(t, isClearValue("   "))
	assert.False(t, isClearValue("0"))
	assert.False(t, isClearValue(float64(0)))
}

func intField(id int64) VisibleField { return VisibleField{ID: id, Key: "points", Kind: FieldKindInt} }

func dateField(id int64) VisibleField { return VisibleField{ID: id, Key: "due", Kind: FieldKindDate} }

func textField(id int64) VisibleField { return VisibleField{ID: id, Key: "notes", Kind: FieldKindText} }

func selectField(id int64, options ...string) VisibleField {
	return VisibleField{ID: id, Key: "severity", Kind: FieldKindSelect, Options: options}
}

func TestPlanningParseFieldValueParsesIntFromNumberOrNumericString(t *testing.T) {
	write, err := parseFieldValue(intField(1), float64(5))
	require.NoError(t, err)
	assert.EqualValues(t, 5, write.valueInt)

	write, err = parseFieldValue(intField(1), "42")
	require.NoError(t, err)
	assert.EqualValues(t, 42, write.valueInt)

	_, err = parseFieldValue(intField(1), "not a number")
	require.Error(t, err)
	assert.Equal(t, "bad_value", hubCode(t, err))

	_, err = parseFieldValue(intField(1), "12abc")
	require.Error(t, err, "a numeric prefix is not a numeric string")
	assert.Equal(t, "bad_value", hubCode(t, err))

	_, err = parseFieldValue(intField(1), true)
	require.Error(t, err)
	assert.Equal(t, "bad_value", hubCode(t, err))
}

func TestPlanningParseFieldValueParsesDateFromDayOrRFC3339(t *testing.T) {
	write, err := parseFieldValue(dateField(2), "2026-03-01")
	require.NoError(t, err)
	want, parseErr := time.Parse("2006-01-02", "2026-03-01")
	require.NoError(t, parseErr)
	assert.EqualValues(t, want.Unix(), write.valueUnix)

	write, err = parseFieldValue(dateField(2), "2026-03-01T12:00:00Z")
	require.NoError(t, err)
	want2, parseErr := time.Parse(time.RFC3339, "2026-03-01T12:00:00Z")
	require.NoError(t, parseErr)
	assert.EqualValues(t, want2.Unix(), write.valueUnix)

	_, err = parseFieldValue(dateField(2), "not a date")
	require.Error(t, err)
	assert.Equal(t, "bad_value", hubCode(t, err))
}

func TestPlanningParseFieldValueEnforcesTheTextByteLimit(t *testing.T) {
	atLimit := strings.Repeat("a", maxFieldTextBytes)
	write, err := parseFieldValue(textField(3), atLimit)
	require.NoError(t, err)
	assert.Equal(t, atLimit, write.valueText)

	overLimit := strings.Repeat("a", maxFieldTextBytes+1)
	_, err = parseFieldValue(textField(3), overLimit)
	require.Error(t, err)
	assert.Equal(t, "bad_value", hubCode(t, err))
}

func TestPlanningParseFieldValueRequiresOneOfTheDeclaredOptions(t *testing.T) {
	field := selectField(4, "low", "high")
	write, err := parseFieldValue(field, "low")
	require.NoError(t, err)
	assert.Equal(t, "low", write.valueText)

	_, err = parseFieldValue(field, "medium")
	require.Error(t, err)
	require.Equal(t, "bad_option", hubCode(t, err))
	var hubErr interface{ Error() string }
	require.ErrorAs(t, err, &hubErr)
	assert.Contains(t, hubErr.Error(), "low")
}

func TestPlanningPointsOfReadsTheIntAccessorOrZero(t *testing.T) {
	assert.Equal(t, 0, PointsOf(nil))
	assert.Equal(t, 0, PointsOf(map[string]any{}))
	assert.Equal(t, 0, PointsOf(map[string]any{"points": "not an int64"}))
	assert.Equal(t, 5, PointsOf(map[string]any{"points": int64(5)}))
}
