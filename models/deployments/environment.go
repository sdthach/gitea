// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package deployments holds the fork's environment, deployment, approval and audit
// aggregates, plus secret scoping and job-environment resolution, one file each.
package deployments

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"gitea.dev/models/db"
	hub_model "gitea.dev/models/hub"
	"gitea.dev/modules/timeutil"

	"xorm.io/builder"
)

// Approval policies. A new environment defaults to PolicyNone, so adding the fork
// changes no existing behaviour until a policy is set.
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

// Environment is authoritative for which environments exist and what is scoped to them.
// Column order is configuration read at render time; the model expresses no sequence beyond
// the predecessor link below.
type Environment struct {
	ID                int64              `xorm:"pk autoincr" json:"id"`
	RepoID            int64              `xorm:"INDEX UNIQUE(repo_name) NOT NULL DEFAULT 0" json:"repo_id"`
	Name              string             `xorm:"VARCHAR(64) UNIQUE(repo_name) NOT NULL" json:"name"`
	SortOrder         int64              `xorm:"NOT NULL DEFAULT 0" json:"sort_order"`
	ApprovalPolicy    string             `xorm:"VARCHAR(32) NOT NULL DEFAULT 'none'" json:"approval_policy"`
	RequiredApprovals int64              `xorm:"NOT NULL DEFAULT 1" json:"required_approvals"`
	CreatedUnix       timeutil.TimeStamp `xorm:"created NOT NULL" json:"created_unix"`
	UpdatedUnix       timeutil.TimeStamp `xorm:"updated NOT NULL" json:"updated_unix"`

	// The sequence policy and its bypass. RequirePredecessor defaults to false,
	// so an environment with no policy only warns and never refuses.
	// The three allowlist fields are branch protection's own, spelled exactly as
	// models/git/protected_branch.go:46-48 spells them, so no gate models permission twice.
	// BlockAdminOverride is upstream's BlockAdminMergeOverride without the "Merge",
	// which names a step no deploy has; see promotion.go. RequireFullRelease is per
	// environment rather than a list of names: an operator names environments whatever
	// suits them, so which of them takes an unfinished build is a property of the row.
	Predecessor            string  `xorm:"VARCHAR(64) NOT NULL DEFAULT ''" json:"predecessor"`
	RequirePredecessor     bool    `xorm:"NOT NULL DEFAULT false" json:"require_predecessor"`
	RequireFullRelease     bool    `xorm:"NOT NULL DEFAULT false" json:"require_full_release"`
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
// a suggested next action.
func ValidateEnvironment(env *Environment) error {
	if NormalizeEnvironmentName(env.Name) == "" {
		return &hub_model.Error{
			Message:         "environment name is empty",
			SuggestedAction: "Give the environment a name, for example \"prod\".",
		}
	}
	if len(env.Name) > 64 {
		return &hub_model.Error{
			Message:         fmt.Sprintf("environment name %q is %d characters, the maximum is 64", env.Name, len(env.Name)),
			SuggestedAction: "Shorten the name to 64 characters or fewer.",
		}
	}
	if !isKnownPolicy(env.ApprovalPolicy) {
		return &hub_model.Error{
			Message:         fmt.Sprintf("%q is not an approval policy", env.ApprovalPolicy),
			SuggestedAction: "Use one of: " + strings.Join(ApprovalPolicies, ", ") + ".",
		}
	}
	if env.RequiredApprovals < 1 {
		return &hub_model.Error{
			Message:         fmt.Sprintf("required_approvals is %d, which would make the gate unsatisfiable", env.RequiredApprovals),
			SuggestedAction: "Set required_approvals to 1 or more, or set approval_policy to \"none\" to remove the gate.",
		}
	}
	return ValidatePromotionPolicy(env) // the sequence rule, in promotion.go
}

func isKnownPolicy(policy string) bool { return slices.Contains(ApprovalPolicies, policy) }

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
		return nil, &hub_model.Error{
			Message:         fmt.Sprintf("no environment named %q", name),
			SuggestedAction: "List /api/deployments/v1/environments to see the environments that exist.",
		}
	}
	return GetEnvironment(ctx, DefaultsRepoID, name)
}

// GetEnvironmentByID reads one environment by primary key.
func GetEnvironmentByID(ctx context.Context, id int64) (*Environment, error) {
	env := new(Environment)
	has, err := db.GetEngine(ctx).ID(id).Get(env)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, &hub_model.Error{
			Message:         fmt.Sprintf("no environment with id %d", id),
			SuggestedAction: "List environments to see the ones that exist.",
		}
	}
	return env, nil
}

// CreateEnvironment inserts one environment row after validation.
func CreateEnvironment(ctx context.Context, env *Environment) error {
	if env.ID != 0 {
		return &hub_model.Error{
			Message:         "cannot create an environment with a preset id",
			SuggestedAction: "Omit the id field; it is assigned by the database.",
		}
	}
	env.Name = NormalizeEnvironmentName(env.Name)
	NormalizePromotionPolicy(env)
	if env.ApprovalPolicy == "" {
		env.ApprovalPolicy = PolicyNone
	}
	if env.RequiredApprovals < 1 {
		env.RequiredApprovals = 1
	}
	if err := ValidateEnvironment(env); err != nil {
		return err
	}
	return db.Insert(ctx, env)
}

// UpdateEnvironment replaces one environment row after validation.
func UpdateEnvironment(ctx context.Context, env *Environment) error {
	if env.ID <= 0 {
		return &hub_model.Error{
			Message:         "cannot update an environment without an id",
			SuggestedAction: "Pass the environment's id from a previous GET.",
		}
	}
	env.Name = NormalizeEnvironmentName(env.Name)
	NormalizePromotionPolicy(env)
	if err := ValidateEnvironment(env); err != nil {
		return err
	}
	_, err := db.GetEngine(ctx).ID(env.ID).AllCols().Update(env)
	return err
}

// DeleteEnvironment removes one environment row.
func DeleteEnvironment(ctx context.Context, id int64) error {
	affected, err := db.GetEngine(ctx).ID(id).Delete(new(Environment))
	if err != nil {
		return err
	}
	if affected == 0 {
		return &hub_model.Error{
			Message:         fmt.Sprintf("no environment with id %d", id),
			SuggestedAction: "List environments to see the ones that exist.",
		}
	}
	return nil
}
