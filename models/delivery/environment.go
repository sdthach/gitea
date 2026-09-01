// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package delivery

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"

	"xorm.io/builder"
)

// Approval policies (F5). A new environment defaults to PolicyNone, so adding the fork
// changes no existing behaviour until a policy is set (F5b). Slice 6 gives them meaning.
const (
	PolicyNone        = "none"
	PolicyAnyApprover = "any_approver"
	PolicyOthersOnly  = "others_only"
)

// ApprovalPolicies is the complete policy set, declared once.
var ApprovalPolicies = []string{PolicyNone, PolicyAnyApprover, PolicyOthersOnly}

// DefaultsRepoID marks the instance-wide default environment set. A row with this RepoID
// is a template: a repository that has declared no environment of its own renders these.
const DefaultsRepoID int64 = 0

// Environment is authoritative for which environments exist and what is scoped to them (F9).
// Column order is configuration read at render time; the model expresses no sequence beyond
// the predecessor slice 5 adds.
type Environment struct {
	ID                int64              `xorm:"pk autoincr" json:"id"`
	RepoID            int64              `xorm:"INDEX UNIQUE(repo_name) NOT NULL DEFAULT 0" json:"repo_id"`
	Name              string             `xorm:"VARCHAR(64) UNIQUE(repo_name) NOT NULL" json:"name"`
	SortOrder         int64              `xorm:"NOT NULL DEFAULT 0" json:"sort_order"`
	ApprovalPolicy    string             `xorm:"VARCHAR(32) NOT NULL DEFAULT 'none'" json:"approval_policy"`
	RequiredApprovals int64              `xorm:"NOT NULL DEFAULT 1" json:"required_approvals"`
	CreatedUnix       timeutil.TimeStamp `xorm:"created NOT NULL" json:"created_unix"`
	UpdatedUnix       timeutil.TimeStamp `xorm:"updated NOT NULL" json:"updated_unix"`

	// The sequence policy (E17) and its bypass (F10). RequirePredecessor defaults to false,
	// so an environment with no policy behaves as it did before slice 5 — a warning only
	// (F11). The three allowlist fields are branch protection's own, spelled exactly as
	// models/git/protected_branch.go:46-48 spells them, so no gate models permission twice
	// (F12). BlockAdminOverride is upstream's BlockAdminMergeOverride without the "Merge",
	// which names a step no deploy has; see promotion.go.
	Predecessor            string  `xorm:"VARCHAR(64) NOT NULL DEFAULT ''" json:"predecessor"`
	RequirePredecessor     bool    `xorm:"NOT NULL DEFAULT false" json:"require_predecessor"`
	BlockAdminOverride     bool    `xorm:"NOT NULL DEFAULT false" json:"block_admin_override"`
	EnableBypassAllowlist  bool    `xorm:"NOT NULL DEFAULT false" json:"enable_bypass_allowlist"`
	BypassAllowlistUserIDs []int64 `xorm:"JSON TEXT" json:"bypass_allowlist_user_ids"`
	BypassAllowlistTeamIDs []int64 `xorm:"JSON TEXT" json:"bypass_allowlist_team_ids"`
}

// TableName keeps every fork table under one prefix, so no fork table can collide with an
// upstream one a later pin introduces.
func (*Environment) TableName() string { return "delivery_environment" }

func init() {
	db.RegisterModel(new(Environment))
}

// NormalizeEnvironmentName is the single spelling rule for an environment name. Names are
// identifiers, so they are compared and stored lower-cased.
func NormalizeEnvironmentName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ValidateEnvironment refuses a row the API would otherwise persist. Every message carries
// a suggested next action (A21).
func ValidateEnvironment(env *Environment) error {
	if NormalizeEnvironmentName(env.Name) == "" {
		return &Error{
			Message:         "environment name is empty",
			SuggestedAction: "Give the environment a name, for example \"prod\".",
		}
	}
	if len(env.Name) > 64 {
		return &Error{
			Message:         fmt.Sprintf("environment name %q is %d characters, the maximum is 64", env.Name, len(env.Name)),
			SuggestedAction: "Shorten the name to 64 characters or fewer.",
		}
	}
	if !isKnownPolicy(env.ApprovalPolicy) {
		return &Error{
			Message:         fmt.Sprintf("%q is not an approval policy", env.ApprovalPolicy),
			SuggestedAction: "Use one of: " + strings.Join(ApprovalPolicies, ", ") + ".",
		}
	}
	if env.RequiredApprovals < 1 {
		return &Error{
			Message:         fmt.Sprintf("required_approvals is %d, which would make the gate unsatisfiable", env.RequiredApprovals),
			SuggestedAction: "Set required_approvals to 1 or more, or set approval_policy to \"none\" to remove the gate.",
		}
	}
	return ValidatePromotionPolicy(env) // the sequence rule, in promotion.go (E17, F11)
}

func isKnownPolicy(policy string) bool { return slices.Contains(ApprovalPolicies, policy) }

// Error is a hub error. It always carries a suggested next action (A21).
type Error struct {
	Message         string
	SuggestedAction string
}

func (e *Error) Error() string { return e.Message + " — " + e.SuggestedAction }

// FindEnvironments lists environments matching cond, ordered by orderBy, with the
// grammar's own paging applied by the caller.
func FindEnvironments(ctx context.Context, cond builder.Cond, orderBy string, limit, offset int) ([]*Environment, int64, error) {
	sess := db.GetEngine(ctx).Where(cond).OrderBy(orderBy)
	if limit > 0 {
		sess = sess.Limit(limit, offset)
	}
	envs := make([]*Environment, 0, 8)
	count, err := sess.FindAndCount(&envs)
	if err != nil {
		return nil, 0, err
	}
	return envs, count, nil
}

// GetEnvironment reads one environment by repository and name, falling back to the
// instance-wide default set when the repository has declared none of its own.
func GetEnvironment(ctx context.Context, repoID int64, name string) (*Environment, error) {
	name = NormalizeEnvironmentName(name)
	env := new(Environment)
	has, err := db.GetEngine(ctx).Where("repo_id = ? AND name = ?", repoID, name).Get(env)
	if err != nil {
		return nil, err
	}
	if has {
		return env, nil
	}
	if repoID == DefaultsRepoID {
		return nil, &Error{
			Message:         fmt.Sprintf("no environment named %q", name),
			SuggestedAction: "List /api/delivery/v1/environments to see the environments that exist.",
		}
	}
	return GetEnvironment(ctx, DefaultsRepoID, name)
}
