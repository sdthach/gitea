// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	issues_model "gitea.dev/models/issues"
	planning_model "gitea.dev/models/planning"
	"gitea.dev/models/unittest"
	planningv1 "gitea.dev/routers/api/planning/v1"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fieldRowPayload is the shape the field CRUD endpoints answer with.
type fieldRowPayload struct {
	ID       int64    `json:"id"`
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Kind     string   `json:"kind"`
	Options  []string `json:"options"`
	Required bool     `json:"required"`
	Sort     int      `json:"sort"`
	Scope    string   `json:"scope"`
	ScopeID  int64    `json:"scope_id"`
}

func createField(t *testing.T, token string, body map[string]any) fieldRowPayload {
	t.Helper()
	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/fields", body).AddTokenAuth(token)
	var row fieldRowPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &row)
	return row
}

// issueFieldsFacetsPayload is the shape GET /issues/{id} and PUT /issues/{id}/fields answer
// with, reduced to the fields this file's tests assert on.
type issueFieldsFacetsPayload struct {
	IssueID int64 `json:"issue_id"`
	Fields  []struct {
		ID  int64  `json:"id"`
		Key string `json:"key"`
	} `json:"fields"`
	Values map[string]any `json:"values"`
}

func setIssueFieldsRaw(t *testing.T, token string, issueID int64, body map[string]any) (*issueFieldsFacetsPayload, hubRefusal, int) {
	t.Helper()
	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/issues/"+strconv.FormatInt(issueID, 10)+"/fields", body).AddTokenAuth(token)
	resp := MakeRequest(t, req, NoExpectedStatus)
	if resp.Code == http.StatusOK {
		facets := DecodeJSON(t, resp, &issueFieldsFacetsPayload{})
		return facets, hubRefusal{}, resp.Code
	}
	refusal := DecodeJSON(t, resp, &hubRefusal{})
	return nil, *refusal, resp.Code
}

func getIssueValues(t *testing.T, token string, issueID int64) map[string]any {
	t.Helper()
	req := NewRequest(t, "GET", planningv1.BasePath+"/issues/"+strconv.FormatInt(issueID, 10)+"/fields").AddTokenAuth(token)
	var payload issueFieldsFacetsPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &payload)
	return payload.Values
}

// TestPlanningFieldCRUDAsRepoAdmin covers create, update and delete, each reply carrying the
// row it changed.
func TestPlanningFieldCRUDAsRepoAdmin(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	created := createField(t, token, map[string]any{
		"repo_id": 1, "key": "severity", "label": "Severity", "kind": "select", "options": []string{"low", "high"},
	})
	assert.Equal(t, "severity", created.Key)
	assert.Equal(t, "repo", created.Scope)
	assert.EqualValues(t, 1, created.ScopeID)
	unittest.AssertExistsAndLoadBean(t, &planning_model.Field{ID: created.ID, RepoID: 1, Key: "severity"})

	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/fields/"+strconv.FormatInt(created.ID, 10),
		map[string]any{"key": "severity", "label": "Severity level", "kind": "select", "options": []string{"low", "medium", "high"}}).AddTokenAuth(token)
	var updated fieldRowPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &updated)
	assert.Equal(t, "Severity level", updated.Label)
	assert.Equal(t, []string{"low", "medium", "high"}, updated.Options)

	req = NewRequest(t, "DELETE", planningv1.BasePath+"/fields/"+strconv.FormatInt(created.ID, 10)).AddTokenAuth(token)
	var deleted struct {
		DeletedValues int64 `json:"deleted_values"`
	}
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &deleted)
	assert.Zero(t, deleted.DeletedValues, "no issue ever carried a value for it")
	unittest.AssertNotExistsBean(t, &planning_model.Field{ID: created.ID})
}

// TestPlanningFieldCreateRefusedForAReader is the write's authorization check: user4 can read
// user2/repo1, which is public, and does not administer it. Nothing is written. Dropping the
// scope-admin check in CreateField turns this red.
func TestPlanningFieldCreateRefusedForAReader(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	readerToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/fields",
		map[string]any{"repo_id": 1, "key": "severity", "label": "Severity", "kind": "text"}).AddTokenAuth(readerToken)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusForbidden), &refusal)
	assert.Equal(t, "forbidden", refusal.Code)
	assert.NotEmpty(t, refusal.SuggestedAction, "every error carries a suggested next action")

	unittest.AssertNotExistsBean(t, &planning_model.Field{RepoID: 1, Key: "severity"})
}

// TestPlanningFieldsRefusesAnInvisiblePrivateRepoWithout404NamingIt: repo2 is private and
// user4 cannot see it, so naming it is 404 rather than 403, and the refusal never confirms
// the repository exists.
func TestPlanningFieldsRefusesAnInvisiblePrivateRepoWithout404NamingIt(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	readerToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

	req := NewRequest(t, "GET", planningv1.BasePath+"/fields?repo_id=2").AddTokenAuth(readerToken)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusNotFound), &refusal)
	assert.Equal(t, "repo_not_found", refusal.Code)
	assert.NotContains(t, refusal.Message, "repo2", "the refusal never names the private repository")
}

// TestPlanningFieldCreateRefusesBadKeyDuplicateKeyAndBadOptions covers three of the create
// refusals in one test, each writing nothing.
func TestPlanningFieldCreateRefusesBadKeyDuplicateKeyAndBadOptions(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	createField(t, token, map[string]any{"repo_id": 1, "key": "dup", "label": "Dup", "kind": "int"})
	countBefore, err := db.GetEngine(t.Context()).Count(new(planning_model.Field))
	require.NoError(t, err)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/fields",
		map[string]any{"repo_id": 1, "key": "dup", "label": "Dup again", "kind": "int"}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "field_exists", refusal.Code)

	req = NewRequestWithJSON(t, "POST", planningv1.BasePath+"/fields",
		map[string]any{"repo_id": 1, "key": "Not-A-Slug", "label": "Bad key", "kind": "int"}).AddTokenAuth(token)
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "bad_key", refusal.Code)

	req = NewRequestWithJSON(t, "POST", planningv1.BasePath+"/fields",
		map[string]any{"repo_id": 1, "key": "noopts", "label": "No options", "kind": "select", "options": []string{}}).AddTokenAuth(token)
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "options_required", refusal.Code)

	countAfter, err := db.GetEngine(t.Context()).Count(new(planning_model.Field))
	require.NoError(t, err)
	assert.Equal(t, countBefore, countAfter, "none of the three refused creates wrote a row")
}

// TestPlanningFieldUpdateRefusesChangingKind: kind is fixed at creation, since values may
// already exist under it. Dropping this check turns the test red.
func TestPlanningFieldUpdateRefusesChangingKind(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	field := createField(t, token, map[string]any{"repo_id": 1, "key": "points", "label": "Points", "kind": "int"})

	req := NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/fields/"+strconv.FormatInt(field.ID, 10),
		map[string]any{"key": "points", "label": "Points", "kind": "text"}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "kind_immutable", refusal.Code)

	stillInt := unittest.AssertExistsAndLoadBean(t, &planning_model.Field{ID: field.ID})
	assert.Equal(t, "int", stillInt.Kind, "the refused update left the field's kind untouched")
}

// TestPlanningFieldDeleteCascadesItsValuesAndReportsTheCount.
func TestPlanningFieldDeleteCascadesItsValuesAndReportsTheCount(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	field := createField(t, token, map[string]any{"repo_id": 1, "key": "points", "label": "Points", "kind": "int"})
	require.NoError(t, planning_model.UpsertValue(t.Context(), &planning_model.FieldValue{IssueID: 1, FieldID: field.ID, ValueInt: 3}))
	require.NoError(t, planning_model.UpsertValue(t.Context(), &planning_model.FieldValue{IssueID: 2, FieldID: field.ID, ValueInt: 5}))

	req := NewRequest(t, "DELETE", planningv1.BasePath+"/fields/"+strconv.FormatInt(field.ID, 10)).AddTokenAuth(token)
	var deleted struct {
		DeletedValues int64 `json:"deleted_values"`
	}
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &deleted)
	assert.EqualValues(t, 2, deleted.DeletedValues)

	unittest.AssertNotExistsBean(t, &planning_model.Field{ID: field.ID})
	unittest.AssertNotExistsBean(t, &planning_model.FieldValue{FieldID: field.ID})
}

// fieldsRoadmapPayload is GET /roadmap's shape, reduced to what this file's tests assert on.
type fieldsRoadmapPayload struct {
	Bars []struct {
		IssueID int64          `json:"issue_id"`
		Points  int            `json:"points"`
		Fields  map[string]any `json:"fields"`
	} `json:"bars"`
	Rollups []struct {
		Kind         string `json:"kind"`
		Key          string `json:"key"`
		PointsTotal  int    `json:"points_total"`
		PointsClosed int    `json:"points_closed"`
	} `json:"rollups"`
	Groups []struct {
		Key          string `json:"key"`
		PointsTotal  int    `json:"points_total"`
		PointsClosed int    `json:"points_closed"`
	} `json:"groups"`
}

// fieldsBoardPayload is GET /board's shape, reduced to what this file's tests assert on.
type fieldsBoardPayload struct {
	Groups []struct {
		Columns []struct {
			Cards []struct {
				IssueID int64          `json:"issue_id"`
				Points  int            `json:"points"`
				Fields  map[string]any `json:"fields"`
			} `json:"cards"`
		} `json:"columns"`
	} `json:"groups"`
}

// TestPlanningIssueSetFieldsShowsOnBoardCardRoadmapBarAndRollups covers the values reaching
// every published projection: the board card, the roadmap bar, and — with a parent and one
// closed child — the parent rollup and the parent group, each summing points over their own
// children. Mutating points_closed to count open issues instead turns the rollup and group
// assertions here red, the same as the pure unit test in services/planning.
func TestPlanningIssueSetFieldsShowsOnBoardCardRoadmapBarAndRollups(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	createField(t, token, map[string]any{"repo_id": 1, "key": "points", "label": "Points", "kind": "int"})
	createField(t, token, map[string]any{"repo_id": 1, "key": "sev", "label": "Severity", "kind": "select", "options": []string{"low", "high"}})

	epic := issueType(t, 1, "epic-fields", "#8250df", "octicon-rocket", 1)
	story := issueType(t, 1, "story-fields", "#2da44e", "octicon-tasklist", 3)
	setIssueType(t, token, "user2/repo1", 1, epic.ID) // issue 1 becomes the root: it is already on project board 1
	setIssueType(t, token, "user2/repo1", 5, story.ID)
	setIssueParent(t, token, "user2/repo1", 5, 1)

	// Issue 5 is already closed in the fixtures, and issue 1, the parent, stays open.
	unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 5, IsClosed: true})

	facets, refusal, status := setIssueFieldsRaw(t, token, 1, map[string]any{"repo": "user2/repo1", "values": map[string]any{"points": 5, "sev": "high"}})
	require.Equal(t, http.StatusOK, status, "%+v", refusal)
	assert.EqualValues(t, 5, facets.Values["points"])
	assert.Equal(t, "high", facets.Values["sev"])

	_, refusal, status = setIssueFieldsRaw(t, token, 5, map[string]any{"repo": "user2/repo1", "values": map[string]any{"points": 3}})
	require.Equal(t, http.StatusOK, status, "%+v", refusal)

	var board fieldsBoardPayload
	req := NewRequest(t, "GET", planningv1.BasePath+"/board?repo_id=1&project_id=1").AddTokenAuth(token)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &board)
	found := false
	for _, group := range board.Groups {
		for _, column := range group.Columns {
			for _, card := range column.Cards {
				if card.IssueID == 1 {
					found = true
					assert.Equal(t, 5, card.Points)
					assert.EqualValues(t, 5, card.Fields["points"])
					assert.Equal(t, "high", card.Fields["sev"])
				}
			}
		}
	}
	assert.True(t, found, "issue 1 is on project board 1")

	var roadmap fieldsRoadmapPayload
	req = NewRequest(t, "GET", planningv1.BasePath+"/roadmap?repo_id=1&limit=200&group_by=parent").AddTokenAuth(token)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &roadmap)

	barFound := false
	for _, bar := range roadmap.Bars {
		if bar.IssueID == 1 {
			barFound = true
			assert.Equal(t, 5, bar.Points)
			assert.EqualValues(t, 5, bar.Fields["points"])
		}
	}
	assert.True(t, barFound, "issue 1 draws a bar")

	rollupFound := false
	for _, rollup := range roadmap.Rollups {
		if rollup.Kind == "parent" && rollup.Key == "1" {
			rollupFound = true
			assert.Equal(t, 3, rollup.PointsTotal, "the parent rollup sums only its children — issue 5's 3 points")
			assert.Equal(t, 3, rollup.PointsClosed, "issue 5 is closed")
		}
	}
	assert.True(t, rollupFound, "issue 1 seeds a parent rollup")

	groupFound := false
	for _, group := range roadmap.Groups {
		if group.Key == "1" {
			groupFound = true
			assert.Equal(t, 8, group.PointsTotal, "the group sums the whole subtree: 5 + 3")
			assert.Equal(t, 3, group.PointsClosed, "only issue 5 is closed")
		}
	}
	assert.True(t, groupFound, "issue 1 seeds a parent group")
}

// TestPlanningIssueSetFieldsPartialUpdateLeavesOtherValues covers the partial update, and that
// unknown_field and bad_option each write nothing — dropping the transaction in SetIssueFields
// so the good key writes before the refusal turns this red.
func TestPlanningIssueSetFieldsPartialUpdateLeavesOtherValues(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	createField(t, token, map[string]any{"repo_id": 1, "key": "budget", "label": "Budget", "kind": "int"})
	createField(t, token, map[string]any{"repo_id": 1, "key": "prio", "label": "Priority", "kind": "select", "options": []string{"low", "high"}})

	_, refusal, status := setIssueFieldsRaw(t, token, 3, map[string]any{"repo": "user2/repo1", "values": map[string]any{"budget": 10, "prio": "low"}})
	require.Equal(t, http.StatusOK, status, "%+v", refusal)

	// A partial update naming only budget leaves prio exactly as it stood.
	facets, refusal, status := setIssueFieldsRaw(t, token, 3, map[string]any{"repo": "user2/repo1", "values": map[string]any{"budget": 20}})
	require.Equal(t, http.StatusOK, status, "%+v", refusal)
	assert.EqualValues(t, 20, facets.Values["budget"])
	assert.Equal(t, "low", facets.Values["prio"], "untouched by the partial update")

	// unknown_field: budget sorts before unknownkey, so a dropped transaction would still let
	// budget become 30 here.
	_, refusal, status = setIssueFieldsRaw(t, token, 3, map[string]any{"repo": "user2/repo1", "values": map[string]any{"budget": 30, "unknownkey": "x"}})
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "unknown_field", refusal.Code)
	assert.Contains(t, refusal.Message, "unknownkey")

	// bad_option: budget again sorts before prio, so this is the pair a dropped transaction
	// would actually let write through.
	_, refusal, status = setIssueFieldsRaw(t, token, 3, map[string]any{"repo": "user2/repo1", "values": map[string]any{"budget": 40, "prio": "medium"}})
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "bad_option", refusal.Code)

	values := getIssueValues(t, token, 3)
	assert.EqualValues(t, 20, values["budget"], "neither refused write changed budget")
	assert.Equal(t, "low", values["prio"])
}

// TestPlanningIssueSetFieldsRefusesClearingARequiredField.
func TestPlanningIssueSetFieldsRefusesClearingARequiredField(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	createField(t, token, map[string]any{"repo_id": 1, "key": "mandatory", "label": "Mandatory", "kind": "int", "required": true})

	_, refusal, status := setIssueFieldsRaw(t, token, 3, map[string]any{"repo": "user2/repo1", "values": map[string]any{"mandatory": 5}})
	require.Equal(t, http.StatusOK, status, "%+v", refusal)

	_, refusal, status = setIssueFieldsRaw(t, token, 3, map[string]any{"repo": "user2/repo1", "values": map[string]any{"mandatory": nil}})
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "required_field", refusal.Code)

	values := getIssueValues(t, token, 3)
	assert.EqualValues(t, 5, values["mandatory"], "the refused clear left the value in place")
}

// TestPlanningIssueSetFieldsTextBoundaryAt4096Bytes: 4096 bytes accepted, 4097 refused and
// leaves the prior value in place. Widening the limit turns this red.
func TestPlanningIssueSetFieldsTextBoundaryAt4096Bytes(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	createField(t, token, map[string]any{"repo_id": 1, "key": "notes", "label": "Notes", "kind": "text"})

	atLimit := strings.Repeat("a", 4096)
	facets, refusal, status := setIssueFieldsRaw(t, token, 3, map[string]any{"repo": "user2/repo1", "values": map[string]any{"notes": atLimit}})
	require.Equal(t, http.StatusOK, status, "%+v", refusal)
	assert.Equal(t, atLimit, facets.Values["notes"])

	overLimit := strings.Repeat("a", 4097)
	_, refusal, status = setIssueFieldsRaw(t, token, 3, map[string]any{"repo": "user2/repo1", "values": map[string]any{"notes": overLimit}})
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "bad_value", refusal.Code)

	values := getIssueValues(t, token, 3)
	assert.Equal(t, atLimit, values["notes"], "the refused write left the 4KiB value in place")
}

// TestPlanningIssueSetFieldsAcceptsValuesAsAJSONString: the CLI sends every body member as a
// string, so values may arrive as a JSON string holding an object rather than the object
// itself.
func TestPlanningIssueSetFieldsAcceptsValuesAsAJSONString(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	createField(t, token, map[string]any{"repo_id": 1, "key": "points", "label": "Points", "kind": "int"})

	facets, refusal, status := setIssueFieldsRaw(t, token, 3, map[string]any{"repo": "user2/repo1", "values": `{"points": 9}`})
	require.Equal(t, http.StatusOK, status, "%+v", refusal)
	assert.EqualValues(t, 9, facets.Values["points"])
}

// TestPlanningIssueSetFieldsRefusedForAReaderWritesNothing: user4 can read user2/repo1, which is
// public, but has no write access to its Issues unit, so the write is 403 and leaves no value
// behind. Dropping the write check in issueFieldsTarget turns this red.
func TestPlanningIssueSetFieldsRefusedForAReaderWritesNothing(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	adminToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	field := createField(t, adminToken, map[string]any{"repo_id": 1, "key": "points", "label": "Points", "kind": "int"})

	readerToken := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)
	_, refusal, status := setIssueFieldsRaw(t, readerToken, 1, map[string]any{"repo": "user2/repo1", "values": map[string]any{"points": 5}})
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "forbidden", refusal.Code)

	unittest.AssertNotExistsBean(t, &planning_model.FieldValue{IssueID: 1, FieldID: field.ID})
}

// TestPlanningFieldCreateRefusesPointsUnderAnyKindButInt: rollups sum points as one int, so
// neither create nor update may give the key a different kind. Nothing is written by either
// refused call.
func TestPlanningFieldCreateRefusesPointsUnderAnyKindButInt(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)

	req := NewRequestWithJSON(t, "POST", planningv1.BasePath+"/fields",
		map[string]any{"repo_id": 1, "key": "points", "label": "Points", "kind": "text"}).AddTokenAuth(token)
	var refusal hubRefusal
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "bad_kind", refusal.Code)
	unittest.AssertNotExistsBean(t, &planning_model.Field{RepoID: 1, Key: "points"})

	field := createField(t, token, map[string]any{"repo_id": 1, "key": "note", "label": "Note", "kind": "text"})
	req = NewRequestWithJSON(t, "PUT", planningv1.BasePath+"/fields/"+strconv.FormatInt(field.ID, 10),
		map[string]any{"key": "points", "label": "Points", "kind": "text"}).AddTokenAuth(token)
	DecodeJSON(t, MakeRequest(t, req, http.StatusUnprocessableEntity), &refusal)
	assert.Equal(t, "bad_kind", refusal.Code)
	stillNote := unittest.AssertExistsAndLoadBean(t, &planning_model.Field{ID: field.ID})
	assert.Equal(t, "note", stillNote.Key, "the refused rename left the field untouched")
}

// TestPlanningFieldsShadowingOnTheShippedPath covers FieldsFor's nearest-scope shadowing end to
// end: a repo-scoped points shadows the instance's, GET /fields?repo_id publishes only the
// nearer row, and a value set through the API on the repo field — never the instance one set
// directly through the model — is what the board card carries. Passing 0, 0 into shadowFields
// (services/planning/field.go) turns this red.
func TestPlanningFieldsShadowingOnTheShippedPath(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	adminToken := getTokenForLoggedInUser(t, loginUser(t, "user1"), auth_model.AccessTokenScopeAll)
	instanceField := createField(t, adminToken, map[string]any{"key": "points", "label": "Points", "kind": "int"})
	assert.Equal(t, "instance", instanceField.Scope)

	repoToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	repoField := createField(t, repoToken, map[string]any{"repo_id": 1, "key": "points", "label": "Points", "kind": "int"})
	assert.Equal(t, "repo", repoField.Scope)

	req := NewRequest(t, "GET", planningv1.BasePath+"/fields?repo_id=1").AddTokenAuth(repoToken)
	var rows []fieldRowPayload
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &rows)
	pointsRows := 0
	for _, row := range rows {
		if row.Key == "points" {
			pointsRows++
			assert.Equal(t, "repo", row.Scope, "the repo-scoped row shadows the instance one")
			assert.Equal(t, repoField.ID, row.ID)
		}
	}
	assert.Equal(t, 1, pointsRows, "exactly one points row is published, never both")

	require.NoError(t, planning_model.UpsertValue(t.Context(), &planning_model.FieldValue{IssueID: 1, FieldID: instanceField.ID, ValueInt: 111}))
	_, refusal, status := setIssueFieldsRaw(t, repoToken, 1, map[string]any{"repo": "user2/repo1", "values": map[string]any{"points": 222}})
	require.Equal(t, http.StatusOK, status, "%+v", refusal)

	var board fieldsBoardPayload
	req = NewRequest(t, "GET", planningv1.BasePath+"/board?repo_id=1&project_id=1").AddTokenAuth(repoToken)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &board)
	found := false
	for _, group := range board.Groups {
		for _, column := range group.Columns {
			for _, card := range column.Cards {
				if card.IssueID == 1 {
					found = true
					assert.Equal(t, 222, card.Points, "the repo field's value, never the instance one")
					assert.EqualValues(t, 222, card.Fields["points"])
				}
			}
		}
	}
	assert.True(t, found, "issue 1 is on project board 1")
}
