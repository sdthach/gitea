import {request} from '../../modules/fetch.ts';
import type {Check, Deployment, Insights, MatrixRow, Promotion, Release, RepoStat, Review, Run, TrendPoint} from './types.ts';

export type DeploymentsApiConfig = {
  apiBase: string;
  appSubUrl: string;
  // token is the page's own minted token (empty when signed out, or omitted entirely by a page
  // that never mints one, as insights and the release badges fragment do not). A value saved
  // in this tab's sessionStorage takes over from it; see storedToken.
  token?: string;
};

// paths is every operation path the deployments pages fetch, spelled exactly as
// docs/deployments/openapi.json spells them, so a page names the endpoint it is a client of
// rather than a rewritten copy of it. {name} placeholders are filled in by withParam below.
export const paths = {
  matrix: '/deployments/matrix',
  deployments: '/deployments',
  deploymentChecks: '/deployments/{id}/checks',
  releases: '/repos/{owner}/{repo}/releases',
  reviews: '/reviews',
  reviewApprove: '/reviews/{id}/approve',
  reviewReject: '/reviews/{id}/reject',
  insights: '/insights',
  insightsTrends: '/insights/trends',
  insightsRepos: '/insights/repos',
  runs: '/runs',
};

function withParam(template: string, name: string, value: string | number): string {
  return template.replace(`{${name}}`, String(value));
}

function query(params: Record<string, string | number | undefined>): string {
  const usp = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') usp.set(key, String(value));
  }
  return usp.toString();
}

export class ApiError extends Error {
  status: number;
  suggestedAction: string;
  code: string;

  constructor(message: string, status: number, suggestedAction: string, code: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.suggestedAction = suggestedAction;
    this.code = code;
  }
}

// tokenKey is the sessionStorage slot every deployments page keeps its pasted token under, so
// switching between the matrix, a review list and the confirm page carries one token, not one
// per tab reload.
const tokenKey = 'deployments-api-token';

// storedToken prefers a token pasted into this tab over the page's own minted one: a page
// mints a token scoped to its own writes, but a reader who was not offered one (the release
// badges fragment) or whose minted token expired can still paste one in.
export function storedToken(config: DeploymentsApiConfig): string {
  try {
    return window.sessionStorage.getItem(tokenKey) || config.token || '';
  } catch {
    return config.token || '';
  }
}

// saveToken reports false when this browser refuses to keep site data for the tab, which the
// caller shows as its own failure rather than a silent no-op.
export function saveToken(value: string): boolean {
  try {
    window.sessionStorage.setItem(tokenKey, value);
    return true;
  } catch {
    return false;
  }
}

type CallOptions = {
  method?: string;
  body?: Record<string, unknown>;
};

// call carries the stored token on every request, reads included: a page's reads already ride
// the browser session, and a token alongside it is harmless, but a write MUST have one, which
// is why every page mints one for a signed-in viewer up front.
export async function call<T>(config: DeploymentsApiConfig, path: string, {method = 'GET', body}: CallOptions = {}): Promise<T> {
  const headers: Record<string, string> = {accept: 'application/json'};
  const token = storedToken(config);
  if (token) headers.authorization = `token ${token}`;

  const resp = await request(`${config.apiBase}${path}`, {
    method,
    headers,
    credentials: 'same-origin',
    ...(body !== undefined && {data: body}),
  });

  const text = await resp.text();
  let data: Record<string, unknown>;
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    data = {message: `the API returned ${resp.status}`};
  }

  if (!resp.ok) {
    throw new ApiError(
      typeof data.message === 'string' ? data.message : `the API returned ${resp.status}`,
      resp.status,
      typeof data.suggested_action === 'string' ? data.suggested_action : '',
      typeof data.code === 'string' ? data.code : '',
    );
  }
  return data as T;
}

export function getMatrix(config: DeploymentsApiConfig, limit = 50): Promise<MatrixRow[]> {
  return call<MatrixRow[]>(config, `${paths.matrix}?${query({limit})}`);
}

export function getDeploymentHistory(config: DeploymentsApiConfig, opts: {repoId: number; releaseTag: string; limit?: number}): Promise<Deployment[]> {
  const qs = query({repo_id: opts.repoId, release_tag: opts.releaseTag, expand: 'audit', limit: opts.limit ?? 200});
  return call<Deployment[]>(config, `${paths.deployments}?${qs}`);
}

// getWaitingDeployments lists the checks-pending placeholders across every repository the
// caller can see, the reviews page's own second source of "why is this deploy not moving":
// distinct from a review-gate hold, a waiting deployment has not reached the reviewer check
// yet, so it never appears in getReviews.
export function getWaitingDeployments(config: DeploymentsApiConfig, opts: {environment?: string; limit?: number} = {}): Promise<Deployment[]> {
  const qs = query({status: 'waiting', environment: opts.environment, sort_by: 'id', order: 'desc', limit: opts.limit ?? 200});
  return call<Deployment[]>(config, `${paths.deployments}?${qs}`);
}

// findWaitingDeployment resolves the placeholder row a `waiting` matrix cell or review-page
// entry stands for, so its id can be passed to getDeploymentChecks: neither a matrix cell nor
// a review row carries a deployment id of its own, only the run and release identity.
export function findWaitingDeployment(config: DeploymentsApiConfig, opts: {repoId: number; environment: string; releaseTag: string}): Promise<Deployment[]> {
  const qs = query({repo_id: opts.repoId, environment: opts.environment, release_tag: opts.releaseTag, status: 'waiting', limit: 1});
  return call<Deployment[]>(config, `${paths.deployments}?${qs}`);
}

export function getDeploymentChecks(config: DeploymentsApiConfig, deploymentId: number): Promise<Check[]> {
  return call<Check[]>(config, withParam(paths.deploymentChecks, 'id', deploymentId));
}

export function getReleases(config: DeploymentsApiConfig, repoFullName: string, tagName: string): Promise<Release[]> {
  const [owner, repo] = repoFullName.split('/');
  const path = withParam(withParam(paths.releases, 'owner', owner), 'repo', repo);
  return call<Release[]>(config, `${path}?${query({tag_name: tagName})}`);
}

export function getReviews(config: DeploymentsApiConfig, opts: {environment?: string; limit?: number} = {}): Promise<Review[]> {
  const qs = query({environment: opts.environment, sort_by: 'created_unix', order: 'desc', limit: opts.limit ?? 200});
  return call<Review[]>(config, `${paths.reviews}?${qs}`);
}

export function approveReview(config: DeploymentsApiConfig, reviewId: number): Promise<Review> {
  return call<Review>(config, withParam(paths.reviewApprove, 'id', reviewId), {method: 'POST'});
}

export function rejectReview(config: DeploymentsApiConfig, reviewId: number): Promise<Review> {
  return call<Review>(config, withParam(paths.reviewReject, 'id', reviewId), {method: 'POST'});
}

// planOrConfirmDeployment does not throw on a non-2xx status: a refused, override-required or
// checks-failed plan is a normal Promotion body carried on a 4xx status (CreateDeployment's own
// choice of status per outcome), not an Error — the confirm page renders it as the plan it is,
// so the status is returned alongside the body rather than swallowed by call()'s Error handling.
export async function planOrConfirmDeployment(config: DeploymentsApiConfig, body: {
  repo: string; environment: string; release_tag: string; confirm: boolean; override_reason?: string;
}): Promise<{status: number; payload: Promotion}> {
  const headers: Record<string, string> = {accept: 'application/json'};
  const token = storedToken(config);
  if (token) headers.authorization = `token ${token}`;
  const resp = await request(`${config.apiBase}${paths.deployments}`, {
    method: 'POST', headers, credentials: 'same-origin', data: body,
  });
  return {status: resp.status, payload: await resp.json()};
}

export function getInsights(config: DeploymentsApiConfig, windowDays: number | string): Promise<Insights> {
  return call<Insights>(config, `${paths.insights}?${query({window_days: windowDays})}`);
}

export function getRuns(config: DeploymentsApiConfig, opts: {limit?: number; status?: string} = {}): Promise<Run[]> {
  const qs = query({limit: opts.limit ?? 10, sort_by: 'created_unix', order: 'desc', 'status[eq]': opts.status});
  return call<Run[]>(config, `${paths.runs}?${qs}`);
}

export function getInsightsRepos(config: DeploymentsApiConfig, windowDays: number | string, limit = 10): Promise<RepoStat[]> {
  const qs = query({window_days: windowDays, limit, sort_by: 'runs', order: 'desc'});
  return call<RepoStat[]>(config, `${paths.insightsRepos}?${qs}`);
}

export function getInsightsTrends(config: DeploymentsApiConfig, windowDays: number | string): Promise<TrendPoint[]> {
  return call<TrendPoint[]>(config, `${paths.insightsTrends}?${query({window_days: windowDays})}`);
}
