// Types mirror docs/deployments/openapi.json field-for-field: JSON keys stay snake_case here
// rather than being renamed to camelCase, so a field name is one grep away from its schema.

// ReviewsPageConfig is deployments/reviews.tmpl's ctx.PageData shape: the base API config plus
// the environment name a repository-scoped /environments/{name}/reviews link narrows to.
export type ReviewsPageConfig = {
  apiBase: string;
  appSubUrl: string;
  token: string;
  environmentName: string;
};

// InsightsPageConfig carries no token: every insights figure is read with the browser session,
// same as Gitea's own /runs and /workflows pages.
export type InsightsPageConfig = {
  apiBase: string;
  appSubUrl: string;
  defaultWindowDays: number;
};

export type MatrixCell = {
  environment: string;
  sort_order: number;
  state: 'never' | 'live' | 'superseded' | 'failed' | 'in_progress' | 'held' | 'waiting';
  symbol: string;
  successes: number;
  run_id: number;
  run_url: string;
  occurred_unix: number;
};

export type MatrixRow = {
  repo_id: number;
  repo_full_name: string;
  release_tag: string;
  release_url: string;
  created_unix: number;
  cells: MatrixCell[];
};

export type Deployment = {
  id: number;
  repo_id: number;
  environment: string;
  release_tag: string;
  status: string;
  run_id: number;
  run_url: string;
  sha: string;
  branch: string;
  created_unix: number;
  audit?: Array<{event: string; actor_login?: string}>;
};

export type Release = {
  id: number;
  repo_id: number;
  tag_name: string;
  target: string;
  sha: string;
  title: string;
  url: string;
  is_prerelease: boolean;
  created_unix: number;
  artifacts: Array<{name: string; url: string}>;
};

export type Check = {
  name: 'reviewers' | 'prior_deployment' | 'releases_only' | 'wait_timer' | 'deployment_window' |
    'required_status_contexts' | 'exclusive_lock';
  state: 'pass' | 'wait' | 'fail';
  reason?: string;
  retry_at?: number;
  suggested_action?: string;
};

export type Promotion = {
  confirmed: boolean;
  environment: string;
  outcome: 'proceed' | 'warn' | 'override' | 'refuse';
  predecessor_state: 'none' | 'never' | 'held' | 'live';
  release_tag: string;
  repo_full_name: string;
  repo_id: number;
  state: 'planned' | 'refused' | 'override_required' | 'checks_failed' | 'waiting' | 'dispatched';
  checks?: Check[];
  currently_live?: string;
  depends_on?: string[];
  deployment_id?: number;
  is_prerelease?: boolean;
  is_rollback?: boolean;
  message?: string;
  override_reason?: string;
  ref?: string;
  requires_override_reason?: boolean;
  run_id?: number;
  run_url?: string;
  sha?: string;
  suggested_action?: string;
  workflow_id?: string;
  // code is never set on a genuine plan: it appears only when this same status arrives from an
  // auth failure ahead of the handler (the Error schema, not Promotion), which is what tells
  // the confirm page apart a sign-in refusal from the sequence rule's own refuse outcome.
  code?: string;
};

export type Review = {
  id: number;
  repo_id: number;
  environment: string;
  release_tag: string;
  run_id: number;
  run_url: string;
  sha: string;
  job_id: number;
  state: 'pending' | 'approved' | 'rejected';
  requester_id: number;
  requester_login: string;
  required_reviewers: number;
  review_policy: 'none' | 'any_approver' | 'others_only';
  reviews_count: number;
  age_seconds: number;
  created_unix: number;
  can_approve: boolean;
};

export type Run = {
  id: number;
  repo_id: number;
  repo_full_name: string;
  workflow_id: string;
  status: 'success' | 'failure' | 'in_progress' | 'queued' | 'cancelled' | 'skipped' | 'unknown';
  title: string;
  ref: string;
  event: string;
  commit_sha: string;
  index: number;
  run_url: string;
  created_unix: number;
  started_unix: number;
  stopped_unix: number;
  duration_seconds: number;
};

export type Summary = {
  active_repositories: number;
  inactive_repositories: number;
  active_workflows: number;
  disabled_workflows: number;
  total_runs: number;
  runs: Record<string, number>;
  success_rate: number;
  total_duration_seconds: number;
  window: {from_unix: number; to_unix: number; days: number};
};

export type Insights = {
  summary: Summary;
  previous: Summary;
  truncated: boolean;
};

export type RepoStat = {
  repo_id: number;
  repo_full_name: string;
  runs: number;
  successes: number;
  failures: number;
  success_rate: number;
  average_duration_seconds: number;
};

export type TrendPoint = {
  date: string;
  day_unix: number;
  runs: number;
  successes: number;
  failures: number;
  average_duration_seconds: number;
  deployments: number;
};
