// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package deployments

import (
	"context"
	"fmt"
	"slices"
	"strings"

	actions_model "gitea.dev/models/actions"
	"gitea.dev/models/db"
	"gitea.dev/models/deployments/approvalgate"
	hub_model "gitea.dev/models/hub"
	"gitea.dev/modules/log"
	"gitea.dev/modules/timeutil"

	"xorm.io/builder"
)

// The review states. A state is a PROJECTION over the append-only audit log; it is
// never a column. A Review row records that a job is held and who asked for it; every
// review and every rejection is an audit event, which is the same discipline the
// grid already applies to cell state.
const (
	ReviewPending  = "pending"
	ReviewApproved = "approved"
	ReviewRejected = "rejected"
)

// ReviewStates is the complete state set, declared once.
var ReviewStates = []string{ReviewPending, ReviewApproved, ReviewRejected}

// Review is one held deploy: the job a runner may not be given until the environment's
// review policy is satisfied.
//
// The table is APPEND-ONLY, like deployments and audit. One row per (repo, run, job) records
// the hold; nothing about it is ever rewritten. RequesterLogin is denormalized for the same
// reason the audit log denormalizes ActorLogin: deleting the user from Gitea must not erase
// who asked for the deploy.
type Review struct {
	ID             int64              `xorm:"pk autoincr" json:"id"`
	RepoID         int64              `xorm:"INDEX UNIQUE(run_job) NOT NULL" json:"repo_id"`
	Environment    string             `xorm:"VARCHAR(64) INDEX NOT NULL" json:"environment"`
	RunID          int64              `xorm:"INDEX UNIQUE(run_job) NOT NULL" json:"run_id"`
	JobID          int64              `xorm:"UNIQUE(run_job) NOT NULL" json:"job_id"`
	ReleaseTag     string             `xorm:"INDEX VARCHAR(255) NOT NULL DEFAULT ''" json:"release_tag"`
	SHA            string             `xorm:"VARCHAR(64) NOT NULL DEFAULT ''" json:"sha"`
	RunURL         string             `xorm:"VARCHAR(255) NOT NULL DEFAULT ''" json:"run_url"`
	RequesterID    int64              `xorm:"INDEX NOT NULL DEFAULT 0" json:"requester_id"`
	RequesterLogin string             `xorm:"VARCHAR(255) NOT NULL DEFAULT ''" json:"requester_login"`
	CreatedUnix    timeutil.TimeStamp `xorm:"created INDEX NOT NULL" json:"created_unix"`
}

// TableName keeps every fork table under one prefix.
func (*Review) TableName() string { return "deploy_review" }

func init() {
	db.RegisterModel(new(Review))
	// Registering here rather than from Init means the gate is live as soon as the package
	// is linked, so no runner poll can slip through between process start and hub mount.
	approvalgate.Register(JobIsHeldForReview)
}

// Vote is one review or rejection reduced to what the decision depends on. It is read
// from the audit log: a review is not stored a second time.
type Vote struct {
	ActorID int64
	Event   string
}

// ProjectReviewState decides whether a held job may run. It is pure, so every policy in
// both its accepting and its refusing case is testable with no database.
//
// Rejecting ends the deploy: a rejection anywhere in the log is terminal and no later
// review revives the run.
func ProjectReviewState(policy string, requiredReviews, requesterID int64, votes []Vote) (string, int64) {
	if policy == "" || policy == PolicyNone {
		// No gate configured, so nothing is held. This is what keeps a fork install
		// behaving exactly as stock Gitea until a policy is set.
		return ReviewApproved, 0
	}

	counted := make(map[int64]bool, len(votes))
	for _, v := range votes {
		if v.Event == AuditRejected {
			return ReviewRejected, 0
		}
		if v.Event != AuditApproved || v.ActorID <= 0 {
			continue
		}
		if policy == PolicyOthersOnly && v.ActorID == requesterID {
			// The requester's own review does not count under others_only. It is
			// refused at the endpoint too, so the rule is enforced rather than hidden.
			continue
		}
		counted[v.ActorID] = true
	}

	// A policy with required_reviewers below one would be unsatisfiable by counting, so it
	// means the same as one. ValidateEnvironment refuses writing such a row; this is the
	// reading side, which cannot assume the row came through it.
	required := max(requiredReviews, 1)
	count := int64(len(counted))
	if count >= required {
		return ReviewApproved, count
	}
	return ReviewPending, count
}

// ValidateReview refuses a row the gate or the API would otherwise persist. Every message
// carries a suggested next action.
func ValidateReview(a *Review) error {
	if a.RepoID <= 0 {
		return &hub_model.Error{
			Message:         fmt.Sprintf("review repo_id is %d", a.RepoID),
			SuggestedAction: "Record the hold against the repository the run belongs to.",
		}
	}
	if NormalizeEnvironmentName(a.Environment) == "" {
		return &hub_model.Error{
			Message:         "review names no environment",
			SuggestedAction: "A job is only held when it declares an environment; set `environment:` on the job, or set the environment's review_policy to \"none\".",
		}
	}
	if a.RunID <= 0 || a.JobID <= 0 {
		return &hub_model.Error{
			Message:         fmt.Sprintf("review names run %d job %d", a.RunID, a.JobID),
			SuggestedAction: "Record the hold against the Actions run and job it holds; the gate needs both to release exactly that job.",
		}
	}
	return nil
}

// AppendReview appends one hold row. It is the only write path to the table: there is no
// update and no delete, so the log of what was held can only grow.
//
// A row carrying a primary key is what an update looks like when written through the model,
// and it is refused rather than silently inserted as a duplicate.
func AppendReview(ctx context.Context, a *Review) error {
	if a.ID != 0 {
		return errAppendOnly("deploy_review", a.ID)
	}
	a.Environment = NormalizeEnvironmentName(a.Environment)
	if err := ValidateReview(a); err != nil {
		return err
	}
	return db.Insert(ctx, a)
}

// FindReviews lists holds matching cond. Holds are finite and stable — one per held job —
// so the resource pages by offset.
func FindReviews(ctx context.Context, cond builder.Cond, orderBy string, limit, offset int) ([]*Review, int64, error) {
	sess := db.GetEngine(ctx).Where(cond).OrderBy(orderBy)
	if limit > 0 {
		sess = sess.Limit(limit, offset)
	}
	rows := make([]*Review, 0, 8)
	count, err := sess.FindAndCount(&rows)
	if err != nil {
		return nil, 0, err
	}
	return rows, count, nil
}

// GetReviewByID reads one hold.
func GetReviewByID(ctx context.Context, id int64) (*Review, error) {
	a := new(Review)
	has, err := db.GetEngine(ctx).ID(id).Get(a)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, &hub_model.Error{
			Message:         fmt.Sprintf("no review %d", id),
			SuggestedAction: "List /api/deployments/v1/reviews to see the deploys that are currently held.",
		}
	}
	return a, nil
}

// VotesForReview reads the reviews and rejections cast against a held deploy out of the
// audit log, oldest first.
func VotesForReview(ctx context.Context, a *Review) ([]Vote, error) {
	cond := builder.Eq{"repo_id": a.RepoID, "environment": a.Environment, "run_id": a.RunID}.
		And(builder.In("event", AuditApproved, AuditRejected))
	rows, err := FindAuditEvents(ctx, cond, "occurred_unix ASC, id ASC", 0)
	if err != nil {
		return nil, err
	}
	votes := make([]Vote, 0, len(rows))
	for _, r := range rows {
		votes = append(votes, Vote{ActorID: r.ActorID, Event: r.Event})
	}
	return votes, nil
}

// ResolveReviewState projects one hold's current state against its environment's live
// policy. The policy is read from the environment record, never inferred from run status,
// which cannot tell a held deploy from a queued one.
func ResolveReviewState(ctx context.Context, a *Review) (state string, count, required int64, err error) {
	env, err := GetEnvironment(ctx, a.RepoID, a.Environment)
	if err != nil {
		return "", 0, 0, err
	}
	votes, err := VotesForReview(ctx, a)
	if err != nil {
		return "", 0, 0, err
	}
	required = max(env.RequiredReviewers, 1)
	state, count = ProjectReviewState(env.ReviewPolicy, required, a.RequesterID, votes)
	return state, count, required, nil
}

// reviewDeps are the lookups the gate performs. They are a struct of functions rather
// than direct calls so that every branch — including the ones that run only when a lookup
// fails, which is where fail-closed lives — is reachable from a unit test with no database,
// no git repository and no network.
type reviewDeps struct {
	repoIsGated       func(ctx context.Context, repoID int64) (bool, error)
	loadJob           func(ctx context.Context, repoID, jobID int64) (*actions_model.ActionRunJob, error)
	environment       func(ctx context.Context, job *actions_model.ActionRunJob) (string, error)
	environmentRecord func(ctx context.Context, repoID int64, name string) (*Environment, error)
	hold              func(ctx context.Context, job *actions_model.ActionRunJob, environment string) (*Review, error)
	votes             func(ctx context.Context, a *Review) ([]Vote, error)
}

// productionReviewDeps is the wiring the running binary uses.
var productionReviewDeps = reviewDeps{
	repoIsGated:       RepoHasGatedEnvironment,
	loadJob:           actions_model.GetRunJobByRepoAndID,
	environment:       JobEnvironment,
	environmentRecord: GetEnvironment,
	hold:              HoldForJob,
	votes:             VotesForReview,
}

// reviewGateDeps is what JobIsHeldForReview calls through. Tests replace it and restore
// it; nothing else writes to it.
var reviewGateDeps = productionReviewDeps

// JobIsHeldForReview is the gate models/actions/task.go delegates to through
// models/deployments/approvalgate — the function the spoke inside CreateTaskForRunner names.
// It runs at job ASSIGNMENT, not at dispatch, so a held job is never handed to a
// runner in the first place rather than being stopped once it is already executing.
//
// It FAILS CLOSED. Every lookup that cannot answer holds the job: an unassigned job is
// retried on the runner's next poll and loses nothing, while a production deploy that ran
// without its review cannot be taken back.
func JobIsHeldForReview(ctx context.Context, repoID, jobID int64) bool {
	gated, err := reviewGateDeps.repoIsGated(ctx, repoID)
	if err != nil {
		log.Error("deployments: read the review policies of repo %d: %v — holding job %d unassigned; check the database is reachable, the runner retries on its next poll", repoID, err, jobID)
		return true
	}
	if !gated {
		// Nothing in this repository is gated, so adding the fork changes no behaviour.
		// This is the branch every ordinary job takes, and it costs one indexed query.
		return false
	}

	job, err := reviewGateDeps.loadJob(ctx, repoID, jobID)
	if err != nil {
		log.Error("deployments: load job %d of repo %d: %v — holding it unassigned; check the database is reachable, the runner retries on its next poll", jobID, repoID, err)
		return true
	}

	environment, err := reviewGateDeps.environment(ctx, job)
	if err != nil {
		log.Error("deployments: resolve the environment of job %d: %v — holding it unassigned; check the workflow file is readable at the run's commit", jobID, err)
		return true
	}
	if environment == "" {
		// A job that declares no environment cannot be gated by one.
		return false
	}

	env, err := reviewGateDeps.environmentRecord(ctx, repoID, environment)
	if err != nil {
		log.Error("deployments: read environment %q of repo %d: %v — holding job %d unassigned; create the environment, or set its review_policy to \"none\"", environment, repoID, err, jobID)
		return true
	}
	if env.ReviewPolicy == "" || env.ReviewPolicy == PolicyNone {
		return false
	}

	hold, err := reviewGateDeps.hold(ctx, job, environment)
	if err != nil {
		log.Error("deployments: record the review hold for job %d in %q: %v — holding it unassigned; check the database is reachable, the runner retries on its next poll", jobID, environment, err)
		return true
	}

	votes, err := reviewGateDeps.votes(ctx, hold)
	if err != nil {
		log.Error("deployments: read the reviews cast on run %d in %q: %v — holding job %d unassigned; check the database is reachable", hold.RunID, environment, err, jobID)
		return true
	}

	state, _ := ProjectReviewState(env.ReviewPolicy, env.RequiredReviewers, hold.RequesterID, votes)
	return state != ReviewApproved
}

// RepoHasGatedEnvironment is the gate's fast path: one indexed query answering whether any
// environment this repository could resolve carries a policy at all. It deliberately reads
// the repository's own rows AND the instance-wide default set without working out which one
// shadows which — over-answering true costs one workflow read, under-answering false would
// let a gated deploy through.
func RepoHasGatedEnvironment(ctx context.Context, repoID int64) (bool, error) {
	return db.GetEngine(ctx).
		Where(builder.In("repo_id", repoID, DefaultsRepoID).
			And(builder.Neq{"review_policy": PolicyNone})).
		Exist(new(Environment))
}

// HoldForJob returns the hold row for a job, appending it the first time the gate sees the
// job. Appending here rather than when the run is created is what closes the race a runner
// polling between run creation and a notifier callback would otherwise win.
//
// Two runners can reach this concurrently for the same job. The unique index on
// (repo_id, run_id, job_id) means one insert loses; the loser returns its error, the gate
// holds the job, and the next poll reads the row the winner wrote.
func HoldForJob(ctx context.Context, job *actions_model.ActionRunJob, environment string) (*Review, error) {
	environment = NormalizeEnvironmentName(environment)
	existing := new(Review)
	has, err := db.GetEngine(ctx).
		Where("repo_id = ? AND run_id = ? AND job_id = ?", job.RepoID, job.RunID, job.ID).
		Get(existing)
	if err != nil {
		return nil, err
	}
	if has {
		return existing, nil
	}

	if err := job.LoadAttributes(ctx); err != nil {
		return nil, err
	}
	run := job.Run
	hold := &Review{
		RepoID:      job.RepoID,
		Environment: environment,
		RunID:       job.RunID,
		JobID:       job.ID,
		ReleaseTag:  releaseTagOfRef(run.Ref),
		SHA:         run.CommitSHA,
		RunURL:      run.HTMLURL(),
	}
	if run.TriggerUser != nil {
		hold.RequesterID, hold.RequesterLogin = run.TriggerUser.ID, run.TriggerUser.Name
	} else {
		hold.RequesterID = run.TriggerUserID
	}
	if err := AppendReview(ctx, hold); err != nil {
		return nil, err
	}
	return hold, nil
}

// releaseTagOfRef reads the release tag out of a run's ref. A deploy dispatched against a
// branch carries no release identity, and the hold simply records none.
func releaseTagOfRef(ref string) string {
	const tagPrefix = "refs/tags/"
	if tag, found := strings.CutPrefix(ref, tagPrefix); found {
		return tag
	}
	return ""
}

// IsReviewState reports whether s is a declared state.
func IsReviewState(s string) bool { return slices.Contains(ReviewStates, s) }
