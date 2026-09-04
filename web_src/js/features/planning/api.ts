import {request} from '../../modules/fetch.ts';
import type {
  Board, CapacityRow, Field, IssueFacets, IssueType, IssueTypeAssignment, MilestoneSchedule, ProjectsPage, ProjectViewList, Roadmap, RoadmapCapacity, Timesheet,
} from './types.ts';

export type PlanningApiConfig = {
  apiBase: string;
  token: string;
};

// v1BasePath is where routers/api/planning/v1 mounts, spelled the way routers/init.go spells
// it. A page outside /planning/* (an issue's own sidebar, an issue list) has no server-rendered
// APIBase of its own, so it builds one from this and window.config.appSubUrl.
const v1BasePath = '/api/planning/v1';

// sessionConfig is a read-only config for a page that authenticates with the browser session
// rather than a minted page token: GET needs no token header (see call, below).
export function sessionConfig(): PlanningApiConfig {
  return {apiBase: `${window.config.appSubUrl}${v1BasePath}`, token: ''};
}

// paths is every operation path the planning page fetches, spelled exactly as
// docs/planning/openapi.json spells them, so a page names the endpoint it is a client of
// rather than a rewritten copy of it. {name} placeholders are filled in by withParam below.
export const paths = {
  board: '/board',
  roadmap: '/roadmap',
  roadmapCapacity: '/roadmap/capacity',
  projects: '/projects',
  projectViews: '/projects/{project_id}/views',
  projectView: '/projects/{project_id}/views/{view_id}',
  issues: '/issues',
  issue: '/issues/{issue_id}',
  issueTypeAssignments: '/issue-type-assignments',
  issueMilestone: '/issues/{issue_id}/milestone',
  issueDates: '/issues/{issue_id}/dates',
  issueType: '/issues/{issue_id}/type',
  issueFields: '/issues/{issue_id}/fields',
  issueEstimate: '/issues/{issue_id}/estimate',
  issueGroup: '/issues/{issue_id}/group',
  issueParent: '/issues/{issue_id}/parent',
  issueTypes: '/issue-types',
  issueTypeById: '/issue-types/{id}',
  fields: '/fields',
  field: '/fields/{id}',
  capacity: '/capacity',
  capacityUser: '/capacity/{user_id}',
  boardCards: '/board/cards',
  boardCardColumn: '/board/cards/{issue_id}/column',
  boardCardGroup: '/board/cards/{issue_id}/group',
  boardColumnOrder: '/board/columns/{column_id}/order',
  milestoneSchedule: '/milestones/{milestone_id}/schedule',
  issueDependencies: '/issues/{issue_id}/dependencies',
  issueDependency: '/issues/{issue_id}/dependencies/{dependency_id}',
  timesheet: '/timesheet',
};

// v1Paths is Gitea's OWN v1 API, spelled exactly as templates/swagger/v1-swagger.generated.json
// spells it — never this fork's hub namespace. The Time tab's writes (adding and deleting a
// tracked-time entry, starting and stopping a stopwatch) go through it directly, with the
// page's own token, because Gitea already has these endpoints and a second copy in the hub
// namespace would be two sources of truth for one issue's tracked time.
export const v1Paths = {
  issueTimes: '/repos/{owner}/{repo}/issues/{index}/times',
  issueTime: '/repos/{owner}/{repo}/issues/{index}/times/{id}',
  stopwatchStart: '/repos/{owner}/{repo}/issues/{index}/stopwatch/start',
  stopwatchStop: '/repos/{owner}/{repo}/issues/{index}/stopwatch/stop',
  userByLogin: '/users/{login}',
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
  if (config.token && method !== 'GET') headers.authorization = `token ${config.token}`;

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

export function getIssueFacets(config: PlanningApiConfig, issueId: number): Promise<IssueFacets> {
  return call<IssueFacets>(config, withParam(paths.issue, 'issue_id', issueId));
}

// getIssueTypeAssignments batches GET /issue-type-assignments: at most 200 issue ids per call,
// enforced by the caller (see batchByRepo in type-icons.ts), not by this function.
export function getIssueTypeAssignments(config: PlanningApiConfig, repoId: number, issueIds: number[]): Promise<IssueTypeAssignment[]> {
  const qs = query({repo_id: repoId, issue_ids: issueIds.join(',')});
  return call<IssueTypeAssignment[]>(config, `${paths.issueTypeAssignments}?${qs}`);
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

export function moveIssueColumn(config: PlanningApiConfig, issueId: number, body: {repo: string; project_id: number; column_id: number; sorting?: number}): Promise<Board> {
  return call<Board>(config, withParam(paths.boardCardColumn, 'issue_id', issueId), {method: 'POST', body});
}

export function moveIssueGroup(config: PlanningApiConfig, issueId: number, body: {repo: string; project_id: number; group_by: string; group?: string}): Promise<Board> {
  return call<Board>(config, withParam(paths.boardCardGroup, 'issue_id', issueId), {method: 'POST', body});
}

export function orderColumn(config: PlanningApiConfig, columnId: number, body: {repo: string; project_id: number; issue_ids: number[]; group_by?: string}): Promise<Board> {
  return call<Board>(config, withParam(paths.boardColumnOrder, 'column_id', columnId), {method: 'POST', body});
}

export function addCard(config: PlanningApiConfig, body: {repo: string; project_id: number; column_id: number; title: string; group_by?: string; group?: string; type_id?: number}): Promise<Board> {
  return call<Board>(config, paths.boardCards, {method: 'POST', body});
}

export function getRoadmapCapacity(config: PlanningApiConfig, opts: {repoId: number; from?: string; to?: string}): Promise<RoadmapCapacity> {
  const qs = query({repo_id: opts.repoId, from: opts.from, to: opts.to});
  return call<RoadmapCapacity>(config, `${paths.roadmapCapacity}?${qs}`);
}

// PlanningScope is the settings page's own scope: a repository, an organization, or, both
// undefined, the instance.
export type PlanningScope = {repoId?: number; orgId?: number};

function scopeQuery(scope: PlanningScope): string {
  return query({repo_id: scope.repoId, org_id: scope.orgId});
}

export function getIssueTypes(config: PlanningApiConfig, scope: PlanningScope): Promise<IssueType[]> {
  return call<IssueType[]>(config, `${paths.issueTypes}?${scopeQuery(scope)}`);
}

export type IssueTypeInput = {repo_id?: number; org_id?: number; name: string; color: string; icon: string; rank: number};

export function createIssueType(config: PlanningApiConfig, body: IssueTypeInput): Promise<IssueType> {
  return call<IssueType>(config, paths.issueTypes, {method: 'POST', body});
}

export function updateIssueType(config: PlanningApiConfig, id: number, body: IssueTypeInput): Promise<IssueType> {
  return call<IssueType>(config, withParam(paths.issueTypeById, 'id', id), {method: 'PUT', body});
}

export function deleteIssueType(config: PlanningApiConfig, id: number, force = false): Promise<IssueType> {
  return call<IssueType>(config, withParam(paths.issueTypeById, 'id', id), {method: 'DELETE', body: {force}});
}

export function getFields(config: PlanningApiConfig, scope: PlanningScope): Promise<Field[]> {
  return call<Field[]>(config, `${paths.fields}?${scopeQuery(scope)}`);
}

export type FieldInput = {repo_id?: number; org_id?: number; key: string; label: string; kind: string; options?: string[]; required: boolean};

export function createField(config: PlanningApiConfig, body: FieldInput): Promise<Field> {
  return call<Field>(config, paths.fields, {method: 'POST', body});
}

export function updateField(config: PlanningApiConfig, id: number, body: FieldInput): Promise<Field> {
  return call<Field>(config, withParam(paths.field, 'id', id), {method: 'PUT', body});
}

export function deleteField(config: PlanningApiConfig, id: number): Promise<{deleted_values: number}> {
  return call(config, withParam(paths.field, 'id', id), {method: 'DELETE'});
}

export function getCapacity(config: PlanningApiConfig, scope: PlanningScope): Promise<CapacityRow[]> {
  return call<CapacityRow[]>(config, `${paths.capacity}?${scopeQuery(scope)}`);
}

export type CapacityInput = {repo_id?: number; org_id?: number; hours_per_day: number; utilization: number; workdays: number};

export function setCapacityUser(config: PlanningApiConfig, userId: number, body: CapacityInput): Promise<CapacityRow> {
  return call<CapacityRow>(config, withParam(paths.capacityUser, 'user_id', userId), {method: 'PUT', body});
}

export function clearCapacityUser(config: PlanningApiConfig, userId: number, scope: PlanningScope): Promise<CapacityRow> {
  return call<CapacityRow>(config, withParam(paths.capacityUser, 'user_id', userId), {method: 'DELETE', body: {repo_id: scope.repoId, org_id: scope.orgId}});
}

// setIssueGroup is the roadmap's own vertical drag: it edits the grouping field directly through
// /issues/{issue_id}/group, distinct from the board's own card group move.
export function setIssueGroup(config: PlanningApiConfig, issueId: number, body: {repo: string; group_by: string; group?: string}): Promise<Roadmap> {
  return call<Roadmap>(config, withParam(paths.issueGroup, 'issue_id', issueId), {method: 'POST', body});
}

export function setIssueParent(config: PlanningApiConfig, issueId: number, body: {repo: string; parent_issue_id: number}): Promise<IssueFacets> {
  return call<IssueFacets>(config, withParam(paths.issueParent, 'issue_id', issueId), {method: 'PUT', body});
}

export function createIssue(config: PlanningApiConfig, body: {
  repo: string; title: string; description?: string; start?: string; end?: string;
  type_id?: number; parent_issue_id?: number; milestone_id?: number; group_by?: string; group?: string;
}): Promise<Roadmap> {
  return call<Roadmap>(config, paths.issues, {method: 'POST', body});
}

export function setMilestoneSchedule(config: PlanningApiConfig, milestoneId: number, body: {repo: string; start: string}): Promise<MilestoneSchedule> {
  return call<MilestoneSchedule>(config, withParam(paths.milestoneSchedule, 'milestone_id', milestoneId), {method: 'PUT', body});
}

export function addIssueDependency(config: PlanningApiConfig, issueId: number, body: {repo: string; depends_on_issue_id: number}): Promise<Roadmap> {
  return call<Roadmap>(config, withParam(paths.issueDependencies, 'issue_id', issueId), {method: 'POST', body});
}

export function removeIssueDependency(config: PlanningApiConfig, issueId: number, dependencyId: number, repo: string): Promise<Roadmap> {
  const path = withParam(withParam(paths.issueDependency, 'issue_id', issueId), 'dependency_id', dependencyId);
  return call<Roadmap>(config, path, {method: 'DELETE', body: {repo}});
}

export function getTimesheet(config: PlanningApiConfig, opts: {repoId: number; from?: string; to?: string; userId?: number}): Promise<Timesheet> {
  const qs = query({repo_id: opts.repoId, from: opts.from, to: opts.to, user_id: opts.userId});
  return call<Timesheet>(config, `${paths.timesheet}?${qs}`);
}

// v1Base derives Gitea's own v1 API root from this fork's own base, which is the only piece of
// server-rendered config a v1 write needs — the page's token already carries write:issue,
// minted for this same page (routers/web/hub.SetPageToken).
function v1Base(config: PlanningApiConfig): string {
  return config.apiBase.replace(/\/api\/planning\/v1$/, '/api/v1');
}

async function v1Call<T>(config: PlanningApiConfig, path: string, {method = 'GET', body}: CallOptions = {}): Promise<T> {
  return call<T>({apiBase: v1Base(config), token: config.token}, path, {method, body});
}

// v1Path fills in a v1Paths template's owner, repo and index placeholders — the trio every one
// of these endpoints addresses an issue by, rather than this fork's own issue_id.
function v1Path(template: string, owner: string, repo: string, index: number): string {
  return template.replace('{owner}', owner).replace('{repo}', repo).replace('{index}', String(index));
}

// addTrackedTime and the writes below are Gitea's OWN time-tracking API: adding and deleting a
// tracked-time entry, and starting and stopping a stopwatch.
export function addTrackedTime(config: PlanningApiConfig, owner: string, repo: string, index: number, body: {time: number; created?: string}): Promise<unknown> {
  return v1Call(config, v1Path(v1Paths.issueTimes, owner, repo, index), {method: 'POST', body});
}

export function deleteTrackedTime(config: PlanningApiConfig, owner: string, repo: string, index: number, timeId: number): Promise<unknown> {
  return v1Call(config, v1Path(v1Paths.issueTime, owner, repo, index).replace('{id}', String(timeId)), {method: 'DELETE'});
}

export function startStopwatch(config: PlanningApiConfig, owner: string, repo: string, index: number): Promise<unknown> {
  return v1Call(config, v1Path(v1Paths.stopwatchStart, owner, repo, index), {method: 'POST'});
}

export function stopStopwatch(config: PlanningApiConfig, owner: string, repo: string, index: number): Promise<unknown> {
  return v1Call(config, v1Path(v1Paths.stopwatchStop, owner, repo, index), {method: 'POST'});
}

// getUserIDByLogin resolves a login to the id PUT /capacity/{user_id} takes, so the capacity
// tab's own create form can name a user by login the way every other field on the page does.
export async function getUserIDByLogin(config: PlanningApiConfig, login: string): Promise<number> {
  const user = await v1Call<{id: number}>(config, v1Paths.userByLogin.replace('{login}', encodeURIComponent(login)));
  return user.id;
}
