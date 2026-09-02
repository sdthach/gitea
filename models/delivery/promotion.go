// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import "fmt"

// The promotion policy is the sequence rule an environment declares about itself:
// which environment must have held a release before this one may take it, whether that is a
// warning or a gate, and who may pass the gate anyway.
//
// The bypass fields on Environment reuse branch protection's own names, defaults and API
// names — EnableBypassAllowlist, BypassAllowlistUserIDs, BypassAllowlistTeamIDs, verified
// at models/git/protected_branch.go:46-48 — because a gate that invents its own notion of
// who may pass is a defect.

// NormalizePromotionPolicy applies the one spelling rule to the policy's own fields. An
// environment name is an identifier, and a predecessor is an environment name, so the two
// are normalized the same way or a predecessor would never match the row it names.
func NormalizePromotionPolicy(env *Environment) {
	env.Predecessor = NormalizeEnvironmentName(env.Predecessor)
}

// ValidatePromotionPolicy refuses a policy the API would otherwise persist. Every message
// carries a suggested next action.
//
// It is called from ValidateEnvironment, so no write path can reach the table around it.
func ValidatePromotionPolicy(env *Environment) error {
	name := NormalizeEnvironmentName(env.Name)
	predecessor := NormalizeEnvironmentName(env.Predecessor)

	if len(predecessor) > 64 {
		return &Error{
			Message:         fmt.Sprintf("predecessor %q is %d characters, the maximum is 64", env.Predecessor, len(predecessor)),
			SuggestedAction: "Name the predecessor environment exactly as its own row spells it, at 64 characters or fewer.",
		}
	}
	if predecessor != "" && predecessor == name {
		return &Error{
			Message:         fmt.Sprintf("environment %q names itself as its predecessor", name),
			SuggestedAction: "Name the environment a release passes through first, for example predecessor \"staging\" on \"prod\", or leave predecessor empty.",
		}
	}
	// require_predecessor with nothing to require is a gate whose condition can never be
	// evaluated. Refusing it here is what keeps it from reading as "always refuse" to one
	// caller and "always pass" to another.
	if env.RequirePredecessor && predecessor == "" {
		return &Error{
			Message:         fmt.Sprintf("environment %q sets require_predecessor but declares no predecessor", name),
			SuggestedAction: "Set predecessor to the environment a release must pass through first, or set require_predecessor to false.",
		}
	}
	return nil
}
