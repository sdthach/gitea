// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"context"
	"slices"

	deployments_model "gitea.dev/models/deployments"
	"gitea.dev/models/organization"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/log"
)

// CanBypassEnvironmentSequence reports whether the user may deploy past an environment's
// sequence rule.
//
// It mirrors CanBypassBranchProtection at models/git/protected_branch.go:213 branch for
// branch, and reads branch protection's own fields under their own names, save one: a
// repository admin passes when AdminsCanBypass is set, otherwise an opt-in allowlist of
// named users and teams decides. Bypass is never admin-only, and no gate in this work
// models permission a second way.
//
// It is fail-CLOSED throughout. Every branch that cannot answer the question returns false,
// including a team lookup that errors: a gate that opens when its own check breaks is worse
// than no gate.
func CanBypassEnvironmentSequence(ctx context.Context, env *deployments_model.Environment, user *user_model.User, isRepoAdmin bool) bool {
	if env == nil || user == nil {
		// Upstream dereferences its user without this guard because its only caller holds a
		// signed-in one. The deployments API is reachable by an anonymous request that got past
		// the router, so the nil case is answered rather than panicked on.
		return false
	}
	if isRepoAdmin && env.AdminsCanBypass {
		return true
	}
	if !env.RestrictReviewers {
		return false
	}
	if slices.Contains(env.ReviewerUserIDs, user.ID) {
		return true
	}
	if len(env.ReviewerTeamIDs) == 0 {
		return false
	}
	in, err := organization.IsUserInTeams(ctx, user.ID, env.ReviewerTeamIDs)
	if err != nil {
		log.Error("IsUserInTeams failed: userID=%d, environment=%q, allowlistTeamIDs=%v, err=%v",
			user.ID, env.Name, env.ReviewerTeamIDs, err)
		return false
	}
	return in
}
