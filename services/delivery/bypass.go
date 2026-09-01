// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"
	"slices"

	delivery_model "gitea.dev/models/delivery"
	"gitea.dev/models/organization"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/log"
)

// CanBypassEnvironmentSequence reports whether the user may deploy past an environment's
// sequence rule (E17, F10).
//
// It mirrors CanBypassBranchProtection at models/git/protected_branch.go:213 branch for
// branch, and reads branch protection's own fields under their own names: a repository
// admin passes unless BlockAdminOverride is set, otherwise an opt-in allowlist of named
// users and teams decides. Bypass is never admin-only, and no gate in this work models
// permission a second way (F12).
//
// It is fail-CLOSED throughout. Every branch that cannot answer the question returns false,
// including a team lookup that errors: a gate that opens when its own check breaks is worse
// than no gate.
func CanBypassEnvironmentSequence(ctx context.Context, env *delivery_model.Environment, user *user_model.User, isRepoAdmin bool) bool {
	if env == nil || user == nil {
		// Upstream dereferences its user without this guard because its only caller holds a
		// signed-in one. The delivery API is reachable by an anonymous request that got past
		// the router, so the nil case is answered rather than panicked on.
		return false
	}
	if isRepoAdmin && !env.BlockAdminOverride {
		return true
	}
	if !env.EnableBypassAllowlist {
		return false
	}
	if slices.Contains(env.BypassAllowlistUserIDs, user.ID) {
		return true
	}
	if len(env.BypassAllowlistTeamIDs) == 0 {
		return false
	}
	in, err := organization.IsUserInTeams(ctx, user.ID, env.BypassAllowlistTeamIDs)
	if err != nil {
		log.Error("IsUserInTeams failed: userID=%d, environment=%q, allowlistTeamIDs=%v, err=%v",
			user.ID, env.Name, env.BypassAllowlistTeamIDs, err)
		return false
	}
	return in
}
