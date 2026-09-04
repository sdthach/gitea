import {createApp} from 'vue';
import type {Environment, EnvironmentsPageConfig, EnvironmentEditPageConfig} from './types.ts';

type EnvironmentsPageDataRoot = {deploymentsEnvironments: EnvironmentsPageConfig};
type EnvironmentEditPageDataRoot = {deploymentsEnvironmentEdit: EnvironmentEditPageConfig};

export async function initDeploymentsEnvironments(el: HTMLElement) {
  const config = (window.config.pageData as unknown as EnvironmentsPageDataRoot).deploymentsEnvironments;
  const {default: EnvironmentsPage} = await import('./EnvironmentsPage.vue');
  createApp(EnvironmentsPage, {config}).mount(el);
}

export async function initDeploymentsEnvironmentEdit(el: HTMLElement) {
  const config = (window.config.pageData as unknown as EnvironmentEditPageDataRoot).deploymentsEnvironmentEdit;
  const {default: EnvironmentEditPage} = await import('./EnvironmentEditPage.vue');
  createApp(EnvironmentEditPage, {config}).mount(el);
}

// normalize fills the array fields the API always sends, but a freshly-built draft (the add()
// functions below) may not carry yet, so every reader can index them without a null check.
export function normalize(env: Environment): Environment {
  return {
    ...env,
    depends_on: [...(env.depends_on ?? [])],
    reviewer_user_ids: [...(env.reviewer_user_ids ?? [])],
    reviewer_team_ids: [...(env.reviewer_team_ids ?? [])],
    required_status_contexts: [...(env.required_status_contexts ?? [])],
  };
}

// payloadOf is the write body PUT /environments/{id} takes: every field the endpoint accepts,
// carried forward unchanged unless the caller's draft touched it. UpdateEnvironmentHandler
// replaces the whole row from this body, so an omitted field here is a field silently cleared —
// the promotion path editor's own writes (auto_promote, wait_minutes, ...) depend on this
// carrying fields the identity and checks forms never touch.
export function payloadOf(env: Environment): Record<string, unknown> {
  return {
    repo_id: env.repo_id,
    name: env.name,
    sort_order: env.sort_order,
    review_policy: env.review_policy,
    required_reviewers: env.required_reviewers,
    depends_on: env.depends_on,
    require_prior_deployment: env.require_prior_deployment,
    releases_only: env.releases_only,
    admins_can_bypass: env.admins_can_bypass,
    restrict_reviewers: env.restrict_reviewers,
    reviewer_user_ids: env.reviewer_user_ids,
    reviewer_team_ids: env.reviewer_team_ids,
    auto_promote: env.auto_promote,
    wait_minutes: env.wait_minutes,
    deploy_window: env.deploy_window,
    required_status_contexts: env.required_status_contexts,
    exclusive_lock: env.exclusive_lock,
  };
}

export type CheckKey = 'reviews' | 'sequence' | 'release_kind' | 'bypass';

// CheckDef is one check's presence, summary and what adding or removing it does to a draft.
// The interactive editor for each (reviewsEditor, sequenceEditor, ...) lives in
// EnvironmentPage.vue: it needs sibling and team lookups no pure function has access to.
export type CheckDef = {
  key: CheckKey;
  label: string;
  present: (env: Environment) => boolean;
  summary: (env: Environment) => string;
  add: (draft: Environment) => void;
  remove: (draft: Environment) => void;
};

export const checks: CheckDef[] = [
  {
    key: 'reviews',
    label: 'Reviews',
    present: (env) => env.review_policy !== 'none',
    summary: (env) => `${env.required_reviewers} review${env.required_reviewers === 1 ? '' : 's'}${
      env.review_policy === 'others_only' ?
        ', and not from whoever asked for the deploy' :
        ', from anyone with write on Actions'}`,
    add: (draft) => {
      draft.review_policy = 'any_approver';
      draft.required_reviewers = 1;
    },
    remove: (draft) => {
      draft.review_policy = 'none';
      draft.required_reviewers = 1;
    },
  },
  {
    key: 'sequence',
    label: 'Sequence',
    present: (env) => env.depends_on.length > 0,
    summary: (env) => `a release must have passed through ${env.depends_on[0]} first${
      env.require_prior_deployment ? '' : ' — a deploy that skips it is warned about, not refused'}`,
    add: () => {}, // the editor picks the dependency; refusing is off until a writer asks for it
    remove: (draft) => {
      draft.depends_on = [];
      draft.require_prior_deployment = false;
    },
  },
  {
    key: 'release_kind',
    label: 'Release kind',
    present: (env) => env.releases_only,
    summary: () => 'prereleases are refused here',
    add: (draft) => {
      draft.releases_only = true;
    },
    remove: (draft) => {
      draft.releases_only = false;
    },
  },
  {
    key: 'bypass',
    label: 'Restricted reviewers',
    present: (env) => env.restrict_reviewers,
    summary: (env) => `${env.reviewer_user_ids.length} user` +
      `${env.reviewer_user_ids.length === 1 ? '' : 's'} and ` +
      `${env.reviewer_team_ids.length} team` +
      `${env.reviewer_team_ids.length === 1 ? '' : 's'} decide${
        env.admins_can_bypass ? '; an administrator may still override' : '; admins cannot override'}`,
    add: (draft) => {
      draft.restrict_reviewers = true;
    },
    remove: (draft) => {
      draft.restrict_reviewers = false;
      draft.reviewer_user_ids = [];
      draft.reviewer_team_ids = [];
      draft.admins_can_bypass = true;
    },
  },
];

// scopeLabel is how the environments list and the promotion path graph both name a repo_id.
export function scopeLabel(repoId: number, repoFullName: string | undefined): string {
  if (!repoId) return 'instance-wide';
  return repoFullName || `repository ${repoId}`;
}
