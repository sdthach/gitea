// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"fmt"

	hub_model "gitea.dev/models/hub"
)

// The promotion policy is the sequence rule an environment declares about itself:
// which environments must have held a release before this one may take it, whether that is a
// warning or a gate, and who may pass the gate anyway.
//
// The bypass fields on Environment reuse branch protection's own names, defaults and API
// names — RestrictReviewers, ReviewerUserIDs, ReviewerTeamIDs, verified
// at models/git/protected_branch.go:46-48 — because a gate that invents its own notion of
// who may pass is a defect.

// NormalizePromotionPolicy applies the one spelling rule to the policy's own fields. An
// environment name is an identifier, and a dependency is an environment name, so both are
// normalized the same way or a dependency would never match the row it names.
func NormalizePromotionPolicy(env *Environment) {
	for i, dep := range env.DependsOn {
		env.DependsOn[i] = NormalizeEnvironmentName(dep)
	}
}

// ValidatePromotionPolicy refuses a policy the API would otherwise persist. Every message
// carries a suggested next action, and names the first dependency it refuses on.
//
// It is called from ValidateEnvironment, so no write path can reach the table around it.
func ValidatePromotionPolicy(env *Environment) error {
	name := NormalizeEnvironmentName(env.Name)

	for _, raw := range env.DependsOn {
		dep := NormalizeEnvironmentName(raw)
		if len(dep) > 64 {
			return &hub_model.Error{
				Message:         fmt.Sprintf("dependency %q is %d characters, the maximum is 64", raw, len(dep)),
				SuggestedAction: "Name the dependency environment exactly as its own row spells it, at 64 characters or fewer.",
			}
		}
		if dep != "" && dep == name {
			return &hub_model.Error{
				Message:         fmt.Sprintf("environment %q names itself in depends_on", name),
				SuggestedAction: "Name the environments a release must pass through first, for example depends_on [\"staging\"] on \"prod\", or leave depends_on empty.",
			}
		}
	}
	// require_prior_deployment with nothing to require is a gate whose condition can never be
	// evaluated. Refusing it here is what keeps it from reading as "always refuse" to one
	// caller and "always pass" to another.
	if env.RequirePriorDeployment && len(env.DependsOn) == 0 {
		return &hub_model.Error{
			Message:         fmt.Sprintf("environment %q sets require_prior_deployment but declares no dependency", name),
			SuggestedAction: "Set depends_on to the environments a release must pass through first, or set require_prior_deployment to false.",
		}
	}
	return nil
}
