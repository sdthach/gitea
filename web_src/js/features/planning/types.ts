// Types mirror docs/planning/openapi.json field-for-field: JSON keys stay snake_case here
// rather than being renamed to camelCase, so a field name is one grep away from its schema.

export type PlanningProjectConfig = {
  apiBase: string;
  token: string;
  repoId: number;
  repoFullName: string;
  projectId: number;
  canWrite: boolean;
  canEditIssues: boolean;
};

export type IssueType = {
  id: number;
  name: string;
  color: string;
  icon: string;
  rank: number;
  sort: number;
  scope: string;
  scope_id: number;
};

export type Field = {
  id: number;
  key: string;
  label: string;
  kind: string;
  options?: string[];
  required: boolean;
  sort: number;
  scope: string;
  scope_id: number;
};

export type LabelRef = {
  id: number;
  name: string;
  color: string;
};

export type TreeEdge = {
  issue_id: number;
  parent_issue_id: number;
};

export type FieldValues = Record<string, unknown>;

export type Card = {
  issue_id: number;
  number: number;
  title: string;
  url: string;
  column_id: number;
  sorting: number;
  type?: string;
  type_id?: number;
  type_color?: string;
  type_icon?: string;
  labels: string[];
  assignees: string[];
  milestone?: string;
  milestone_id?: number;
  is_closed: boolean;
  is_pull: boolean;
  parent_issue_id?: number;
  root_issue_id?: number;
  depth?: number;
  has_children?: boolean;
  fields: FieldValues;
  points: number;
  time_estimate: number;
  tracked_seconds: number;
};

export type Column = {
  column_id: number;
  title: string;
  color?: string;
  default: boolean;
};

export type GroupColumn = {
  column_id: number;
  title: string;
  cards: Card[];
};

export type Group = {
  key: string;
  label: string;
  is_empty_value: boolean;
  columns: GroupColumn[];
  cards: number;
  root_issue_id?: number;
  points_total: number;
  points_closed: number;
};

export type Board = {
  repo_id: number;
  repo_full_name: string;
  project_id: number;
  title?: string;
  group_by: string;
  columns: Column[];
  groups: Group[];
  tree: TreeEdge[];
  types: IssueType[];
  fields: Field[];
  labels: LabelRef[];
  can_write: boolean;
  can_edit_issue: boolean;
};

export type StartSource = 'schedule' | 'issue_created' | 'none';
export type EndSource = 'closed' | 'deadline' | 'effort_estimate';

export type Bar = {
  issue_id: number;
  number: number;
  title: string;
  url: string;
  type?: string;
  type_id?: number;
  type_color?: string;
  type_icon?: string;
  milestone?: string;
  milestone_id?: number;
  start_unix: number;
  end_unix: number;
  labels: string[];
  assignees: string[];
  start_source: StartSource;
  end_source: EndSource;
  end_inferred: boolean;
  is_closed: boolean;
  parent_issue_id?: number;
  root_issue_id?: number;
  depth?: number;
  has_children?: boolean;
  fields: FieldValues;
  points: number;
  time_estimate: number;
  tracked_seconds: number;
};

export type Unmanaged = {
  issue_id: number;
  number: number;
  title: string;
  url: string;
  reason: string;
  suggested_action: string;
  labels: string[];
  assignees: string[];
  type?: string;
  type_id?: number;
  milestone_id?: number;
  is_closed: boolean;
  fields: FieldValues;
  points: number;
  time_estimate: number;
  tracked_seconds: number;
};

export type ArrowKind = 'depends_on' | 'predecessor';

export type Arrow = {
  from_issue_id: number;
  to_issue_id: number;
  kind: ArrowKind;
  enforced: boolean;
  from_rollup?: string;
  to_rollup?: string;
};

export type RollupRow = {
  kind: string;
  key: string;
  label: string;
  type?: string;
  start_unix: number;
  end_unix: number;
  children: number;
  closed: number;
  progress: number;
  end_inferred: boolean;
  partial: boolean;
  issue_id?: number;
  declared_start_unix?: number;
  declared_end_unix?: number;
  contains_children: boolean;
  warning?: string;
  suggested_action?: string;
  points_total: number;
  points_closed: number;
};

export type RoadmapMilestone = {
  milestone_id: number;
  title: string;
  is_closed: boolean;
  start_unix: number;
  end_unix: number;
};

export type RoadmapRulerTick = {
  unix: number;
  label: string;
};

export type RoadmapRuler = {
  unit: string;
  start_unix: number;
  end_unix: number;
  ticks: RoadmapRulerTick[];
};

export type Roadmap = {
  repo_id: number;
  repo_full_name: string;
  bars: Bar[];
  arrows: Arrow[];
  rollups: RollupRow[];
  unmanaged: Unmanaged[];
  group_by: string;
  zoom: string;
  groups: Group[];
  ruler: RoadmapRuler;
  milestones?: RoadmapMilestone[];
  tree: TreeEdge[];
  types: IssueType[];
  fields: Field[];
  labels: LabelRef[];
  can_write?: boolean;
  truncated: boolean;
};

export type ProjectView = {
  id: number;
  project_id: number;
  name: string;
  query: string;
  created_by: number;
  created_unix: number;
};

export type ProjectViewList = {
  views: ProjectView[];
};

export type ProjectsPickerRepo = {
  id: number;
  full_name: string;
  owner: string;
  name: string;
  private: boolean;
  projects_enabled: boolean;
};

export type ProjectsPickerProject = {
  id: number;
  title: string;
  repo_id: number;
  owner_id: number;
  type: number;
  is_closed: boolean;
  columns: number;
};

export type ProjectsPage = {
  repos: ProjectsPickerRepo[];
  projects: ProjectsPickerProject[];
};

export type IssueMilestoneRef = {id: number; title: string; start_unix: number; due_unix: number} | null;
export type IssueParentRef = {issue_id: number; number: number; title: string} | null;
export type IssueTypeRef = {type_id: number; name: string; color: string; icon: string} | null;
export type IssueChildRef = {issue_id: number; number: number; title: string; is_closed: boolean};
export type IssueSchedule = {start_unix: number; start_source: StartSource};
export type IssueProgress = {total: number; closed: number};

export type IssueFacets = {
  issue_id: number;
  repo_id: number;
  number: number;
  can_write: boolean;
  children: IssueChildRef[];
  fields: Field[];
  milestone: IssueMilestoneRef;
  parent: IssueParentRef;
  progress: IssueProgress;
  schedule: IssueSchedule;
  time_estimate: number;
  tracked_seconds: number;
  type: IssueTypeRef;
  types: IssueType[];
  values: FieldValues;
};
