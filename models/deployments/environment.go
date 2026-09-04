// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package deployments holds the fork's environment, deployment, review and audit
// aggregates, plus secret scoping and job-environment resolution, one file each.
package deployments

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"gitea.dev/models/db"
	hub_model "gitea.dev/models/hub"
	"gitea.dev/modules/timeutil"

	"xorm.io/builder"
)

// Review policies. A new environment defaults to PolicyNone, so adding the fork
// changes no existing behaviour until a policy is set.
const (
	PolicyNone        = "none"
	PolicyAnyApprover = "any_approver"
	PolicyOthersOnly  = "others_only"
)

// ReviewPolicies is the complete policy set, declared once.
var ReviewPolicies = []string{PolicyNone, PolicyAnyApprover, PolicyOthersOnly}

// DefaultsRepoID marks the instance-wide default environment set. A row with this RepoID
// is a template: a repository that has declared no environment of its own renders these.
const DefaultsRepoID int64 = 0

// Environment is authoritative for which environments exist and what is scoped to them.
// Column order is configuration read at render time; the model expresses no sequence beyond
// the dependency list below.
type Environment struct {
	ID                int64              `xorm:"pk autoincr" json:"id"`
	RepoID            int64              `xorm:"INDEX UNIQUE(repo_name) NOT NULL DEFAULT 0" json:"repo_id"`
	Name              string             `xorm:"VARCHAR(64) UNIQUE(repo_name) NOT NULL" json:"name"`
	SortOrder         int64              `xorm:"NOT NULL DEFAULT 0" json:"sort_order"`
	ReviewPolicy      string             `xorm:"VARCHAR(32) NOT NULL DEFAULT 'none'" json:"review_policy"`
	RequiredReviewers int64              `xorm:"NOT NULL DEFAULT 1" json:"required_reviewers"`
	CreatedUnix       timeutil.TimeStamp `xorm:"created NOT NULL" json:"created_unix"`
	UpdatedUnix       timeutil.TimeStamp `xorm:"updated NOT NULL" json:"updated_unix"`

	// The sequence policy and its bypass. RequirePriorDeployment defaults to false,
	// so an environment with no policy only warns and never refuses.
	// The three allowlist fields are branch protection's own, spelled exactly as
	// models/git/protected_branch.go:46-48 spells them, so no gate models permission twice.
	// AdminsCanBypass is upstream's BlockAdminMergeOverride without the "Merge" and without
	// the negation — a repository admin passes when this is true, the opposite sense of the
	// column it replaces (migration 3 backfills it as that column's negation); see
	// promotion.go. ReleasesOnly is per
	// environment rather than a list of names: an operator names environments whatever
	// suits them, so which of them takes an unfinished build is a property of the row.
	DependsOn              []string `xorm:"JSON TEXT" json:"depends_on"`
	RequirePriorDeployment bool     `xorm:"NOT NULL DEFAULT false" json:"require_prior_deployment"`
	ReleasesOnly           bool     `xorm:"NOT NULL DEFAULT false" json:"releases_only"`
	AdminsCanBypass        bool     `xorm:"NOT NULL DEFAULT true" json:"admins_can_bypass"`
	RestrictReviewers      bool     `xorm:"NOT NULL DEFAULT false" json:"restrict_reviewers"`
	ReviewerUserIDs        []int64  `xorm:"JSON TEXT" json:"reviewer_user_ids"`
	ReviewerTeamIDs        []int64  `xorm:"JSON TEXT" json:"reviewer_team_ids"`

	// AutoPromote deploys the release this environment last saw one of its dependencies hold
	// live, without a human asking — services/deployments.AutoPromote is the only writer that
	// acts on it. WaitMinutes, DeployWindow, RequiredStatusContexts and ExclusiveLock are the
	// pre-deployment checks services/deployments.EvaluateChecks reads; a zero value on each
	// means the check passes, so adding the fork changes no existing environment's behaviour
	// until an operator sets one.
	AutoPromote            bool          `xorm:"NOT NULL DEFAULT false" json:"auto_promote"`
	WaitMinutes            int           `xorm:"NOT NULL DEFAULT 0" json:"wait_minutes"`
	DeployWindow           *DeployWindow `xorm:"JSON TEXT" json:"deploy_window"`
	RequiredStatusContexts []string      `xorm:"JSON TEXT" json:"required_status_contexts"`
	ExclusiveLock          bool          `xorm:"NOT NULL DEFAULT false" json:"exclusive_lock"`
}

// DeployWindow is the recurring time-of-day window a deploy to this environment must land
// inside. DaysMask bit 0 is Sunday through bit 6 Saturday, so the mask reads the same order
// time.Weekday already does. FromMinute and ToMinute are minutes since local midnight, IN
// Timezone: the window is evaluated in the environment's own zone, not the server's, so a
// change-freeze declared "9am-5pm America/New_York" holds across a daylight-saving transition
// without the environment being re-edited.
//
// A nil DeployWindow, or one with DaysMask zero, means always open — the zero value, so
// adding the column changes no environment's behaviour until an operator sets one.
type DeployWindow struct {
	DaysMask   int    `json:"days_mask"`
	FromMinute int    `json:"from_minute"`
	ToMinute   int    `json:"to_minute"`
	Timezone   string `json:"timezone"`
}

// maxWaitMinutes is a week: a wait timer longer than that is almost certainly a units mistake
// (hours entered as minutes), and the environment editor should refuse it rather than hold
// every deploy for months.
const maxWaitMinutes = 7 * 24 * 60

// maxRequiredStatusContexts bounds the array the environment editor renders as a checklist; a
// deploy needing more than this many green checks has a health problem no gate here fixes.
const maxRequiredStatusContexts = 20

// maxStatusContextLength matches the column git_model.CommitStatus.Context is stored in.
const maxStatusContextLength = 255

// minutesPerDay bounds DeployWindow.FromMinute and ToMinute: a window is minutes since local
// midnight, and 1440 (24:00) is the one value past the last minute of the day that still
// makes a valid boundary, closing a window that runs to midnight.
const minutesPerDay = 24 * 60

// TableName keeps every fork table under one prefix, so no fork table can collide with an
// upstream one a later pin introduces.
func (*Environment) TableName() string { return "deploy_environment" }

func init() {
	db.RegisterModel(new(Environment))
}

// NormalizeEnvironmentName is the single spelling rule for an environment name. Names are
// identifiers, so they are compared and stored lower-cased.
func NormalizeEnvironmentName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// normalizeDependsOn drops every name that normalizes to empty or repeats one already kept.
// NormalizePromotionPolicy only lowercases each entry, so depends_on: [""] or a duplicate
// dependency would otherwise reach the row: EvaluateDependencies then reports the release
// never held in an environment named "", where declaring no dependency at all reports none.
func normalizeDependsOn(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, raw := range names {
		name := NormalizeEnvironmentName(raw)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
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
	if !isKnownPolicy(env.ReviewPolicy) {
		return &hub_model.Error{
			Message:         fmt.Sprintf("%q is not a review policy", env.ReviewPolicy),
			SuggestedAction: "Use one of: " + strings.Join(ReviewPolicies, ", ") + ".",
		}
	}
	if env.RequiredReviewers < 1 {
		return &hub_model.Error{
			Message:         fmt.Sprintf("required_reviewers is %d, which would make the gate unsatisfiable", env.RequiredReviewers),
			SuggestedAction: "Set required_reviewers to 1 or more, or set review_policy to \"none\" to remove the gate.",
		}
	}
	if env.WaitMinutes < 0 || env.WaitMinutes > maxWaitMinutes {
		return &hub_model.Error{
			Message:         fmt.Sprintf("wait_minutes is %d, outside 0..%d", env.WaitMinutes, maxWaitMinutes),
			SuggestedAction: fmt.Sprintf("Set wait_minutes between 0 and %d (a week).", maxWaitMinutes),
			Code:            "bad_wait", Status: http.StatusUnprocessableEntity,
		}
	}
	if err := validateDeployWindow(env.DeployWindow); err != nil {
		return err
	}
	if err := validateRequiredStatusContexts(env.RequiredStatusContexts); err != nil {
		return err
	}
	return ValidatePromotionPolicy(env) // the sequence rule, in promotion.go
}

// validateDeployWindow refuses a window ValidateEnvironment would otherwise persist. A nil
// window, or one whose DaysMask is zero, is "always open" and validates trivially.
func validateDeployWindow(w *DeployWindow) error {
	if w == nil || w.DaysMask == 0 {
		return nil
	}
	if w.DaysMask < 1 || w.DaysMask > 0b111_1111 {
		return &hub_model.Error{
			Message:         fmt.Sprintf("deploy_window days_mask is %d, outside 1..127", w.DaysMask),
			SuggestedAction: "Set days_mask to a combination of the seven day bits (bit 0 Sunday .. bit 6 Saturday), or omit deploy_window for always open.",
			Code:            "bad_window", Status: http.StatusUnprocessableEntity,
		}
	}
	if w.FromMinute < 0 || w.FromMinute > minutesPerDay || w.ToMinute < 0 || w.ToMinute > minutesPerDay {
		return &hub_model.Error{
			Message:         fmt.Sprintf("deploy_window minutes are %d..%d, outside 0..%d", w.FromMinute, w.ToMinute, minutesPerDay),
			SuggestedAction: fmt.Sprintf("Set from_minute and to_minute between 0 and %d.", minutesPerDay),
			Code:            "bad_window", Status: http.StatusUnprocessableEntity,
		}
	}
	if w.FromMinute == w.ToMinute {
		return &hub_model.Error{
			Message:         fmt.Sprintf("deploy_window from_minute and to_minute are both %d, which never opens", w.FromMinute),
			SuggestedAction: "Set from_minute and to_minute to different values. from_minute after to_minute wraps the window past midnight.",
			Code:            "bad_window", Status: http.StatusUnprocessableEntity,
		}
	}
	if _, err := time.LoadLocation(w.Timezone); err != nil {
		return &hub_model.Error{
			Message:         fmt.Sprintf("deploy_window timezone %q is not a recognised IANA name: %v", w.Timezone, err),
			SuggestedAction: `Use an IANA zone name, for example "America/New_York" or "UTC".`,
			Code:            "bad_window", Status: http.StatusUnprocessableEntity,
		}
	}
	return nil
}

// validateRequiredStatusContexts refuses a list ValidateEnvironment would otherwise persist.
func validateRequiredStatusContexts(contexts []string) error {
	if len(contexts) > maxRequiredStatusContexts {
		return &hub_model.Error{
			Message:         fmt.Sprintf("required_status_contexts has %d entries, the maximum is %d", len(contexts), maxRequiredStatusContexts),
			SuggestedAction: fmt.Sprintf("Keep required_status_contexts to %d entries or fewer.", maxRequiredStatusContexts),
			Code:            "bad_contexts", Status: http.StatusUnprocessableEntity,
		}
	}
	for _, c := range contexts {
		if strings.TrimSpace(c) == "" {
			return &hub_model.Error{
				Message:         "required_status_contexts carries an empty entry",
				SuggestedAction: "Remove the empty entry, or name the commit status context it should be, for example \"ci/build\".",
				Code:            "bad_contexts", Status: http.StatusUnprocessableEntity,
			}
		}
		if len(c) > maxStatusContextLength {
			return &hub_model.Error{
				Message:         fmt.Sprintf("required_status_contexts entry %q is %d characters, the maximum is %d", c, len(c), maxStatusContextLength),
				SuggestedAction: fmt.Sprintf("Shorten %q to %d characters or fewer.", c, maxStatusContextLength),
				Code:            "bad_contexts", Status: http.StatusUnprocessableEntity,
			}
		}
	}
	return nil
}

// DependencyGraph validates that every name in envs' own depends_on lists resolves to another
// environment in envs, and that the edges they form together have no cycle. It is pure: envs
// is the effective set the caller has already resolved (repository rows plus the instance
// default any name not overridden falls back to), and no database is read here.
//
// A missing name and an actual cycle answer to the same code: either would let a promotion's
// dependency evaluation loop or dead-end, and an operator fixes both the same way, by editing
// depends_on.
func DependencyGraph(envs []*Environment) error {
	byName := make(map[string]*Environment, len(envs))
	for _, e := range envs {
		byName[NormalizeEnvironmentName(e.Name)] = e
	}
	cycleErr := func(chain []string) error {
		return &hub_model.Error{
			Message:         "depends_on forms a cycle: " + strings.Join(chain, " -> "),
			SuggestedAction: "Remove one edge in the cycle from depends_on.",
			Code:            "cycle", Status: http.StatusUnprocessableEntity,
		}
	}
	const (
		unvisited = iota
		visiting
		done
	)
	state := make(map[string]int, len(byName))
	var visit func(name string, chain []string) error
	visit = func(name string, chain []string) error {
		switch state[name] {
		case done:
			return nil
		case visiting:
			return cycleErr(append(chain, name))
		}
		state[name] = visiting
		for _, raw := range byName[name].DependsOn {
			dep := NormalizeEnvironmentName(raw)
			if _, ok := byName[dep]; !ok {
				return &hub_model.Error{
					Message:         fmt.Sprintf("%s depends on %q, which does not exist", name, dep),
					SuggestedAction: "Create the environment it depends on first, or remove it from depends_on.",
					Code:            "cycle", Status: http.StatusUnprocessableEntity,
				}
			}
			if err := visit(dep, append(chain, name)); err != nil {
				return err
			}
		}
		state[name] = done
		return nil
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	slices.Sort(names) // deterministic error when more than one edge is broken
	for _, name := range names {
		if err := visit(name, nil); err != nil {
			return err
		}
	}
	return nil
}

// EffectiveEnvironmentNames lists every name resolvable for repoID: its own rows plus the
// instance-wide default set, the same two sources GetEnvironment falls back across.
func EffectiveEnvironmentNames(ctx context.Context, repoID int64) ([]string, error) {
	seen := map[string]bool{}
	names := make([]string, 0, 8)
	add := func(cond builder.Cond) error {
		rows := make([]*Environment, 0, 8)
		if err := db.GetEngine(ctx).Where(cond).Cols("name").Find(&rows); err != nil {
			return err
		}
		for _, r := range rows {
			if name := NormalizeEnvironmentName(r.Name); !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
		return nil
	}
	if repoID != DefaultsRepoID {
		if err := add(builder.Eq{"repo_id": repoID}); err != nil {
			return nil, err
		}
	}
	if err := add(builder.Eq{"repo_id": DefaultsRepoID}); err != nil {
		return nil, err
	}
	return names, nil
}

// validateDependencyGraph loads the effective environment set for candidate's repository,
// substitutes candidate for whatever currently holds its name, and refuses a depends_on that
// names an environment outside that set or forms a cycle with it.
func validateDependencyGraph(ctx context.Context, candidate *Environment) error {
	names, err := EffectiveEnvironmentNames(ctx, candidate.RepoID)
	if err != nil {
		return err
	}
	if !slices.Contains(names, candidate.Name) {
		names = append(names, candidate.Name)
	}
	envs := make([]*Environment, 0, len(names))
	for _, name := range names {
		if name == candidate.Name {
			envs = append(envs, candidate)
			continue
		}
		env, err := GetEnvironment(ctx, candidate.RepoID, name)
		if err != nil {
			continue // resolved a moment ago; a concurrent delete is not this write's problem
		}
		envs = append(envs, env)
	}
	return DependencyGraph(envs)
}

func isKnownPolicy(policy string) bool { return slices.Contains(ReviewPolicies, policy) }

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
	env.DependsOn = normalizeDependsOn(env.DependsOn)
	if env.ReviewPolicy == "" {
		env.ReviewPolicy = PolicyNone
	}
	if env.RequiredReviewers < 1 {
		env.RequiredReviewers = 1
	}
	if err := ValidateEnvironment(env); err != nil {
		return err
	}
	if err := validateDependencyGraph(ctx, env); err != nil {
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
	env.DependsOn = normalizeDependsOn(env.DependsOn)
	if err := ValidateEnvironment(env); err != nil {
		return err
	}
	if err := validateDependencyGraph(ctx, env); err != nil {
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
