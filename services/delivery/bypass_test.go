// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"testing"

	delivery_model "gitea.dev/models/delivery"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"

	"github.com/stretchr/testify/assert"
)

// TestDeliveryCanBypassEnvironmentSequence is F10's table, each row in its accepting AND its
// refusing case (J9). It mirrors models/git/protected_branch_test.go's shape because the
// helper mirrors CanBypassBranchProtection: a gate that invents its own notion of who may
// pass is a defect (F12).
//
// The team rows use Gitea's own fixtures: user2 is in team 1 of org 3, user4 is not.
func TestDeliveryCanBypassEnvironmentSequence(t *testing.T) {
	unittest.PrepareTestEnv(t)

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})

	cases := []struct {
		name        string
		env         *delivery_model.Environment
		user        *user_model.User
		isRepoAdmin bool
		want        bool
	}{
		{
			name: "a repository admin passes when the override is not blocked",
			env:  &delivery_model.Environment{Name: "prod"},
			user: user2, isRepoAdmin: true, want: true,
		},
		{
			name: "block_admin_override refuses the same admin",
			env:  &delivery_model.Environment{Name: "prod", BlockAdminOverride: true},
			user: user2, isRepoAdmin: true, want: false,
		},
		{
			name: "a non-admin is refused when the allowlist is off",
			env:  &delivery_model.Environment{Name: "prod"},
			user: user2, want: false,
		},
		{
			name: "an allowlisted user passes",
			env: &delivery_model.Environment{
				Name:                  "prod",
				EnableBypassAllowlist: true, BypassAllowlistUserIDs: []int64{2},
			},
			user: user2, want: true,
		},
		{
			name: "a user not on the allowlist is refused by the same environment",
			env: &delivery_model.Environment{
				Name:                  "prod",
				EnableBypassAllowlist: true, BypassAllowlistUserIDs: []int64{2},
			},
			user: user4, want: false,
		},
		{
			name: "naming users without enabling the allowlist refuses them; the switch is the opt-in",
			env: &delivery_model.Environment{
				Name:                   "prod",
				BypassAllowlistUserIDs: []int64{2},
			},
			user: user2, want: false,
		},
		{
			name: "a member of an allowlisted team passes",
			env: &delivery_model.Environment{
				Name:                  "prod",
				EnableBypassAllowlist: true, BypassAllowlistTeamIDs: []int64{1},
			},
			user: user2, want: true,
		},
		{
			name: "a non-member of the same team is refused",
			env: &delivery_model.Environment{
				Name:                  "prod",
				EnableBypassAllowlist: true, BypassAllowlistTeamIDs: []int64{1},
			},
			user: user4, want: false,
		},
		{
			name: "an admin blocked from overriding still passes through the allowlist",
			env: &delivery_model.Environment{
				Name: "prod", BlockAdminOverride: true,
				EnableBypassAllowlist: true, BypassAllowlistUserIDs: []int64{2},
			},
			user: user2, isRepoAdmin: true, want: true,
		},
		{
			name: "an anonymous caller is refused rather than panicked on",
			env: &delivery_model.Environment{
				Name:                  "prod",
				EnableBypassAllowlist: true, BypassAllowlistUserIDs: []int64{2},
			},
			user: nil, want: false,
		},
		{
			name: "no environment at all is refused",
			env:  nil, user: user2, isRepoAdmin: true, want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want,
				CanBypassEnvironmentSequence(t.Context(), tc.env, tc.user, tc.isRepoAdmin))
		})
	}
}
