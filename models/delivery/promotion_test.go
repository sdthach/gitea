// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"testing"

	"gitea.dev/models/db"
	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeliveryNormalizePromotionPolicyLowerCasesThePredecessor(t *testing.T) {
	env := &Environment{Name: "prod", Predecessor: "  STAGING "}
	NormalizePromotionPolicy(env)
	assert.Equal(t, "staging", env.Predecessor,
		"a predecessor is an environment name, so it is spelled by the same rule or it never matches its row")

	blank := &Environment{Name: "prod", Predecessor: "   "}
	NormalizePromotionPolicy(blank)
	assert.Empty(t, blank.Predecessor)
}

// TestDeliveryValidatePromotionPolicy is the write path's negative case in both directions:
// every refusal AND the accepting case each policy shape reaches.
func TestDeliveryValidatePromotionPolicy(t *testing.T) {
	cases := []struct {
		name    string
		env     *Environment
		refused bool
		says    string
	}{
		{
			name: "no policy at all is accepted, which is what the default means",
			env:  &Environment{Name: "prod"},
		},
		{
			name: "a predecessor without the gate is accepted; it is a warning only",
			env:  &Environment{Name: "prod", Predecessor: "staging"},
		},
		{
			name: "a predecessor with the gate on is accepted",
			env:  &Environment{Name: "prod", Predecessor: "staging", RequirePredecessor: true},
		},
		{
			name:    "an environment naming itself is refused",
			env:     &Environment{Name: "prod", Predecessor: "PROD"},
			refused: true,
			says:    "names itself as its predecessor",
		},
		{
			name:    "the gate on with nothing to require is refused",
			env:     &Environment{Name: "prod", RequirePredecessor: true},
			refused: true,
			says:    "declares no predecessor",
		},
		{
			name:    "a predecessor over 64 characters is refused",
			env:     &Environment{Name: "prod", Predecessor: longName(65)},
			refused: true,
			says:    "the maximum is 64",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePromotionPolicy(tc.env)
			if !tc.refused {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var hubErr *Error
			require.ErrorAs(t, err, &hubErr)
			assert.Contains(t, hubErr.Message, tc.says)
			assert.NotEmpty(t, hubErr.SuggestedAction, "every error carries a suggested next action")
		})
	}
}

// TestDeliveryValidateEnvironmentAppliesThePromotionPolicy pins the wiring rather than the
// helper: ValidateEnvironment is what every write path calls, so a policy rule that the
// exported entry point does not reach is a rule nothing enforces.
func TestDeliveryValidateEnvironmentAppliesThePromotionPolicy(t *testing.T) {
	env := &Environment{Name: "prod", ApprovalPolicy: PolicyNone, RequiredApprovals: 1, RequirePredecessor: true}
	err := ValidateEnvironment(env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declares no predecessor")
}

// TestDeliveryOverrideEventNeedsItsReason covers the write path: an
// override that records no reason answers nothing the log is kept for, so AppendAuditEvent
// refuses it. The accepting case is asserted beside it.
func TestDeliveryOverrideEventNeedsItsReason(t *testing.T) {
	unittest.PrepareTestEnv(t)

	without := &AuditEvent{
		Event: AuditOverridden, Source: SourceUI, RepoID: 1, Environment: "prod",
		ReleaseTag: "v1.0", ActorID: 2, ActorLogin: "user2",
	}
	err := AppendAuditEvent(t.Context(), without)
	require.Error(t, err)
	var hubErr *Error
	require.ErrorAs(t, err, &hubErr)
	assert.Contains(t, hubErr.Message, "carries no reason")
	assert.NotEmpty(t, hubErr.SuggestedAction)

	with := &AuditEvent{
		Event: AuditOverridden, Source: SourceUI, RepoID: 1, Environment: "prod",
		ReleaseTag: "v1.0", ActorID: 2, ActorLogin: "user2", RunID: 77,
		Reason: "hotfix; staging is down",
	}
	require.NoError(t, AppendAuditEvent(t.Context(), with))

	rows, err := FindAuditEvents(t.Context(), builderEq("event", AuditOverridden), "id ASC", 0)
	require.NoError(t, err)
	require.Len(t, rows, 1, "only the row that carried its reason was written")
	assert.Equal(t, "hotfix; staging is down", rows[0].Reason)
}

// TestDeliveryEnvironmentPolicyColumnsRoundTrip measures what an upgraded database looks
// like: Sync adds the sequence-policy columns, and the JSON allowlists read back as empty when they
// hold the NULL an existing row was given. Reading a struct tag is not measuring behaviour.
func TestDeliveryEnvironmentPolicyColumnsRoundTrip(t *testing.T) {
	unittest.PrepareTestEnv(t)

	env := &Environment{
		RepoID: DefaultsRepoID, Name: "policy-round-trip", ApprovalPolicy: PolicyNone, RequiredApprovals: 1,
		Predecessor: "staging", RequirePredecessor: true, BlockAdminOverride: true,
		EnableBypassAllowlist: true, BypassAllowlistUserIDs: []int64{2, 4}, BypassAllowlistTeamIDs: []int64{7},
	}
	require.NoError(t, db.Insert(t.Context(), env))

	read, err := GetEnvironment(t.Context(), DefaultsRepoID, "policy-round-trip")
	require.NoError(t, err)
	assert.Equal(t, "staging", read.Predecessor)
	assert.True(t, read.RequirePredecessor)
	assert.True(t, read.BlockAdminOverride)
	assert.True(t, read.EnableBypassAllowlist)
	assert.Equal(t, []int64{2, 4}, read.BypassAllowlistUserIDs)
	assert.Equal(t, []int64{7}, read.BypassAllowlistTeamIDs)

	// The row an ALTER TABLE left behind: the allowlist columns hold NULL, not '[]'.
	_, err = db.GetEngine(t.Context()).Exec(
		"UPDATE delivery_environment SET bypass_allowlist_user_i_ds = NULL, bypass_allowlist_team_i_ds = NULL WHERE id = ?", env.ID)
	require.NoError(t, err)

	upgraded, err := GetEnvironment(t.Context(), DefaultsRepoID, "policy-round-trip")
	require.NoError(t, err, "a row written before the columns existed still reads")
	assert.Empty(t, upgraded.BypassAllowlistUserIDs)
	assert.Empty(t, upgraded.BypassAllowlistTeamIDs)
}

func longName(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = 'a'
	}
	return string(out)
}
