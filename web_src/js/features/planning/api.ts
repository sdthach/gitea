import {request} from '../../modules/fetch.ts';
import type {
  Board, IssueFacets, ProjectsPage, ProjectViewList, Roadmap,
} from './types.ts';

export type PlanningApiConfig = {
  apiBase: string;
  token: string;
};

// paths is every operation path the planning page fetches, spelled exactly as
// docs/planning/openapi.json spells them, so a page names the endpoint it is a client of
// rather than a rewritten copy of it. {name} placeholders are filled in by withParam below.
export const paths = {
  board: '/board',
  roadmap: '/roadmap',
  projects: '/projects',
  projectViews: '/projects/{project_id}/views',
  projectView: '/projects/{project_id}/views/{view_id}',
  issueMilestone: '/issues/{issue_id}/milestone',
  issueDates: '/issues/{issue_id}/dates',
  issueType: '/issues/{issue_id}/type',
  issueFields: '/issues/{issue_id}/fields',
  issueEstimate: '/issues/{issue_id}/estimate',
};

function withParam(template: string, name: string, value: number): string {
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

  constructor(message: string, status: number, suggestedAction: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.suggestedAction = suggestedAction;
  }
}

type CallOptions = {
  method?: string;
  body?: Record<string, unknown>;
};

export async function call<T>(config: PlanningApiConfig, path: string, {method = 'GET', body}: CallOptions = {}): Promise<T> {
  const headers: Record<string, string> = {accept: 'application/json'};
  if (method !== 'GET' && config.token) headers.authorization = `token ${config.token}`;

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
    );
  }
  return data as T;
}

export function getBoard(config: PlanningApiConfig, opts: {repoId: number; projectId: number; groupBy?: string}): Promise<Board> {
  const qs = query({repo_id: opts.repoId, project_id: opts.projectId, group_by: opts.groupBy});
  return call<Board>(config, `${paths.board}?${qs}`);
}

export function getRoadmap(config: PlanningApiConfig, opts: {repoId: number; groupBy?: string; zoom?: string; parentIssueId?: number; milestoneId?: number}): Promise<Roadmap> {
  const qs = query({
    repo_id: opts.repoId, group_by: opts.groupBy, zoom: opts.zoom,
    parent_issue_id: opts.parentIssueId, milestone_id: opts.milestoneId,
  });
  return call<Roadmap>(config, `${paths.roadmap}?${qs}`);
}

export function getProjects(config: PlanningApiConfig, repoId?: number): Promise<ProjectsPage> {
  const qs = query({repo_id: repoId});
  return call<ProjectsPage>(config, `${paths.projects}?${qs}`);
}

export function getProjectViews(config: PlanningApiConfig, projectId: number, repo: string): Promise<ProjectViewList> {
  const path = withParam(paths.projectViews, 'project_id', projectId);
  return call<ProjectViewList>(config, `${path}?${query({repo})}`);
}

export function createProjectView(config: PlanningApiConfig, projectId: number, body: {name: string; query: string; repo: string}): Promise<ProjectViewList> {
  const path = withParam(paths.projectViews, 'project_id', projectId);
  return call<ProjectViewList>(config, path, {method: 'POST', body});
}

export function deleteProjectView(config: PlanningApiConfig, projectId: number, viewId: number, repo: string): Promise<ProjectViewList> {
  const path = withParam(withParam(paths.projectView, 'project_id', projectId), 'view_id', viewId);
  return call<ProjectViewList>(config, path, {method: 'DELETE', body: {repo}});
}

export function setIssueMilestone(config: PlanningApiConfig, issueId: number, body: {repo: string; milestone_id: number}): Promise<Roadmap> {
  return call<Roadmap>(config, withParam(paths.issueMilestone, 'issue_id', issueId), {method: 'POST', body});
}

export function setIssueDates(config: PlanningApiConfig, issueId: number, body: {repo: string; start?: string; end?: string}): Promise<Roadmap> {
  return call<Roadmap>(config, withParam(paths.issueDates, 'issue_id', issueId), {method: 'POST', body});
}

export function setIssueType(config: PlanningApiConfig, issueId: number, body: {repo: string; type_id: number}): Promise<IssueFacets> {
  return call<IssueFacets>(config, withParam(paths.issueType, 'issue_id', issueId), {method: 'PUT', body});
}

export function clearIssueType(config: PlanningApiConfig, issueId: number, repo: string): Promise<IssueFacets> {
  return call<IssueFacets>(config, withParam(paths.issueType, 'issue_id', issueId), {method: 'DELETE', body: {repo}});
}

export function setIssueFields(config: PlanningApiConfig, issueId: number, body: {repo: string; values: Record<string, unknown>}): Promise<IssueFacets> {
  return call<IssueFacets>(config, withParam(paths.issueFields, 'issue_id', issueId), {method: 'PUT', body});
}

export function setIssueEstimate(config: PlanningApiConfig, issueId: number, body: {repo: string; time_estimate: string}): Promise<IssueFacets> {
  return call<IssueFacets>(config, withParam(paths.issueEstimate, 'issue_id', issueId), {method: 'PUT', body});
}
