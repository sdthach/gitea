// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	delivery "gitea.dev/models/deployments"
	repo_model "gitea.dev/models/repo"
	unit_model "gitea.dev/models/unit"
	"gitea.dev/modules/structs"
	deliveryv1 "gitea.dev/routers/api/delivery/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The roles contrib/delivery-preview/seed.py creates, and what each is permitted to do.
// "approve" holds because prod enables its bypass allowlist naming the approvers team: with
// an allowlist in force the allowlist and repo admins decide alone, so write on Actions
// stops being enough and read-only approvers become enough.
var deliveryRoleMatrix = []struct {
	role                                             string
	readGrid, deploy, approve, setPolicy, seeForeign bool
}{
	{"admin", true, true, true, true, true},
	{"owner", true, true, true, true, false},
	{"maintainer", true, true, true, true, false},
	{"deployer", true, true, false, false, false},
	{"approver", true, false, true, false, false},
	{"reader", true, false, false, false, false},
	{"outsider", false, false, false, false, false},
}

// teamPermission is the org team each role joins. admin is a site administrator and owner
// joins Owners, so neither needs one; outsider joins nothing, which is the whole point.
var deliveryRoleTeams = map[string]string{
	"maintainer": "admin",
	"deployer":   "write",
	"approver":   "read",
	"reader":     "read",
}

type deliveryRoleWorld struct {
	org        string
	repo       *repo_model.Repository
	releaseTag string
	devEnvID   int64
	approvalID int64
	tokens     map[string]string
}

func TestDeliveryPermissionMatrix(t *testing.T) {
	onGiteaRun(t, func(t *testing.T, _ *url.URL) {
		w := setUpDeliveryRoles(t)

		for _, tc := range deliveryRoleMatrix {
			t.Run(tc.role, func(t *testing.T) {
				token := w.tokens[tc.role]
				assert.Equal(t, tc.readGrid, w.gridShowsRepo(t, token), "read grid")
				assert.Equal(t, tc.deploy, w.canDeploy(t, token), "create deployment")
				assert.Equal(t, tc.setPolicy, w.canSetPolicy(t, token), "set environment policy")
				assert.Equal(t, tc.seeForeign, w.seesForeignRepo(t, token), "see another owner's private repo")
				// Approval last: it writes an audit event, and a role that may approve
				// would otherwise change what a later role is answered.
				assert.Equal(t, tc.approve, w.canApprove(t, token), "approve")
			})
		}
	})
}

// gridShowsRepo reports whether the seeded repository's release reaches the caller's grid.
// The endpoint answers 200 for everyone; what differs is whether the row is in it.
func (w *deliveryRoleWorld) gridShowsRepo(t *testing.T, token string) bool {
	t.Helper()
	var rows []struct {
		ReleaseTag string `json:"release_tag"`
	}
	req := NewRequest(t, "GET", deliveryv1.BasePath+"/grid?repo_id="+strconv.FormatInt(w.repo.ID, 10)).
		AddTokenAuth(token)
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &rows)
	for _, row := range rows {
		if row.ReleaseTag == w.releaseTag {
			return true
		}
	}
	return false
}

// canDeploy asks for an unconfirmed deploy to dev, which plans without dispatching, so the
// only thing separating the roles is write on the Actions unit.
func (w *deliveryRoleWorld) canDeploy(t *testing.T, token string) bool {
	t.Helper()
	req := NewRequestWithJSON(t, "POST", deliveryv1.BasePath+"/deployments", map[string]any{
		"repo": w.repo.FullName(), "environment": "dev", "release_tag": w.releaseTag,
	}).AddTokenAuth(token)
	return MakeRequest(t, req, NoExpectedStatus).Code == http.StatusOK
}

func (w *deliveryRoleWorld) canApprove(t *testing.T, token string) bool {
	t.Helper()
	req := NewRequest(t, "POST", fmt.Sprintf("%s/approvals/%d/approve", deliveryv1.BasePath, w.approvalID)).
		AddTokenAuth(token)
	return MakeRequest(t, req, NoExpectedStatus).Code == http.StatusOK
}

func (w *deliveryRoleWorld) canSetPolicy(t *testing.T, token string) bool {
	t.Helper()
	req := NewRequestWithJSON(t, "PUT",
		fmt.Sprintf("%s/environments/%d", deliveryv1.BasePath, w.devEnvID), map[string]any{
			"name": "dev", "sort_order": 10, "approval_policy": "none", "required_approvals": 1,
		}).AddTokenAuth(token)
	return MakeRequest(t, req, NoExpectedStatus).Code == http.StatusOK
}

// seesForeignRepo reads a private repository of an owner none of the roles belong to. Only a
// site administrator gets past Gitea's own visibility check.
func (w *deliveryRoleWorld) seesForeignRepo(t *testing.T, token string) bool {
	t.Helper()
	req := NewRequest(t, "GET", deliveryv1.BasePath+"/repos/user2/repo2/environments").AddTokenAuth(token)
	return MakeRequest(t, req, NoExpectedStatus).Code == http.StatusOK
}

func setUpDeliveryRoles(t *testing.T) *deliveryRoleWorld {
	t.Helper()
	adminToken := getTokenForLoggedInUser(t, loginUser(t, "user1"), auth_model.AccessTokenScopeAll)

	w := &deliveryRoleWorld{
		org: "org-delivery-roles", releaseTag: "release-v1.0.0", tokens: map[string]string{},
	}
	w.tokens["admin"] = adminToken

	req := NewRequestWithJSON(t, "POST", "/api/v1/orgs",
		&structs.CreateOrgOption{UserName: w.org, Visibility: "private"}).AddTokenAuth(adminToken)
	MakeRequest(t, req, http.StatusCreated)

	req = NewRequestWithJSON(t, "POST", "/api/v1/orgs/"+w.org+"/repos",
		&structs.CreateRepoOption{Name: "repo-widget", AutoInit: true, Private: true}).AddTokenAuth(adminToken)
	var created structs.Repository
	DecodeJSON(t, MakeRequest(t, req, http.StatusCreated), &created)
	w.repo = unittestRepo(t, created.ID)

	require.NoError(t, db.Insert(t.Context(), &repo_model.RepoUnit{
		RepoID: w.repo.ID, Type: unit_model.TypeActions, Config: &repo_model.ActionsConfig{},
	}))
	require.NoError(t, db.Insert(t.Context(), &repo_model.Release{
		RepoID: w.repo.ID, PublisherID: 1, TagName: w.releaseTag,
		LowerTagName: strings.ToLower(w.releaseTag), Target: "main",
		Title: w.releaseTag, IsDraft: false, IsPrerelease: false, IsTag: false,
	}))
	// user2/repo2 is private and carries no Actions unit; adding one leaves visibility as
	// the only thing the foreign-repository probe is measuring.
	require.NoError(t, db.Insert(t.Context(), &repo_model.RepoUnit{
		RepoID: 2, Type: unit_model.TypeActions, Config: &repo_model.ActionsConfig{},
	}))

	teams := map[string]int64{}
	for role, permission := range deliveryRoleTeams {
		teams[role] = createDeliveryRoleTeam(t, adminToken, w.org, role, permission)
	}
	for role := range deliveryRoleTeams {
		w.tokens[role] = createDeliveryRoleUser(t, adminToken, role, teams[role])
	}
	w.tokens["outsider"] = createDeliveryRoleUser(t, adminToken, "outsider", 0)
	w.tokens["owner"] = createDeliveryRoleUser(t, adminToken, "owner",
		findDeliveryTeam(t, adminToken, w.org, "Owners"))

	// Repository administrator is a collaboration, not a team: adding a team member writes
	// no access row, and GetDoerRepoPermission raises the caller's access mode from a team
	// only for an owner team. An org team of any permission therefore holds admin on every
	// unit and is still not repo admin.
	adminPerm := structs.RepoWritePermissionAdmin
	req = NewRequestWithJSON(t, "PUT",
		"/api/v1/repos/"+w.repo.FullName()+"/collaborators/"+deliveryRoleLogin("maintainer"),
		&structs.AddCollaboratorOption{Permission: &adminPerm}).AddTokenAuth(adminToken)
	MakeRequest(t, req, http.StatusNoContent)

	dev := &delivery.Environment{
		RepoID: w.repo.ID, Name: "dev", SortOrder: 10,
		ApprovalPolicy: delivery.PolicyNone, RequiredApprovals: 1,
	}
	require.NoError(t, db.Insert(t.Context(), dev))
	w.devEnvID = dev.ID
	require.NoError(t, db.Insert(t.Context(), &delivery.Environment{
		RepoID: w.repo.ID, Name: "prod", SortOrder: 50,
		ApprovalPolicy: delivery.PolicyOthersOnly, RequiredApprovals: 1,
		EnableBypassAllowlist: true, BypassAllowlistTeamIDs: []int64{teams["approver"]},
	}))

	// The requester is the deployer, so others_only also has something to refuse.
	deployer := deliveryRoleLogin("deployer")
	approval := &delivery.Approval{
		RepoID: w.repo.ID, Environment: "prod", RunID: 9101, JobID: 9101,
		ReleaseTag: w.releaseTag, RequesterID: userIDByName(t, adminToken, deployer),
		RequesterLogin: deployer,
	}
	require.NoError(t, db.Insert(t.Context(), approval))
	w.approvalID = approval.ID
	return w
}

func deliveryRoleLogin(role string) string { return "user-" + role + "-0" }

func createDeliveryRoleTeam(t *testing.T, adminToken, org, role, permission string) int64 {
	t.Helper()
	// No units: sending them leaves the team's own access mode at none, so a team meant to
	// be repository administrator holds admin on each unit and is still not repo admin.
	req := NewRequestWithJSON(t, "POST", "/api/v1/orgs/"+org+"/teams", &structs.CreateTeamOption{
		Name: "group-" + role + "s", Description: "preview " + role + " accounts",
		Permission: structs.RepoWritePermission(permission), IncludesAllRepositories: true,
	}).AddTokenAuth(adminToken)
	var team structs.Team
	DecodeJSON(t, MakeRequest(t, req, http.StatusCreated), &team)
	return team.ID
}

func findDeliveryTeam(t *testing.T, adminToken, org, teamName string) int64 {
	t.Helper()
	req := NewRequest(t, "GET", "/api/v1/orgs/"+org+"/teams?limit=100").AddTokenAuth(adminToken)
	var teams []*structs.Team
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &teams)
	for _, team := range teams {
		if team.Name == teamName {
			return team.ID
		}
	}
	t.Fatalf("organization %s has no team %q", org, teamName)
	return 0
}

// createDeliveryRoleUser creates one account, places it in teamID when non-zero, and returns
// a token for it. Passwords are the harness's own, so loginUser can sign the account in.
func createDeliveryRoleUser(t *testing.T, adminToken, role string, teamID int64) string {
	t.Helper()
	login := deliveryRoleLogin(role)
	mustChange := false
	req := NewRequestWithJSON(t, "POST", "/api/v1/admin/users", &structs.CreateUserOption{
		Username: login, Email: login + "@example.test", Password: userPassword,
		FullName: role, MustChangePassword: &mustChange,
	}).AddTokenAuth(adminToken)
	MakeRequest(t, req, http.StatusCreated)

	if teamID != 0 {
		req = NewRequest(t, "PUT", fmt.Sprintf("/api/v1/teams/%d/members/%s", teamID, login)).
			AddTokenAuth(adminToken)
		MakeRequest(t, req, http.StatusNoContent)
	}
	return getTokenForLoggedInUser(t, loginUser(t, login), auth_model.AccessTokenScopeAll)
}

func userIDByName(t *testing.T, adminToken, login string) int64 {
	t.Helper()
	req := NewRequest(t, "GET", "/api/v1/users/"+login).AddTokenAuth(adminToken)
	var user structs.User
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &user)
	return user.ID
}

func unittestRepo(t *testing.T, id int64) *repo_model.Repository {
	t.Helper()
	repo, err := repo_model.GetRepositoryByID(t.Context(), id)
	require.NoError(t, err)
	return repo
}
