import {createApp} from 'vue';
import EnvironmentsPage from './EnvironmentsPage.vue';
import * as Api from './api.ts';
import type {Environment, EnvironmentsPageConfig} from './types.ts';

vi.mock('./api.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof Api>();
  return {
    ...actual,
    getEnvironments: vi.fn(), getEnvironmentPaths: vi.fn(), getRepository: vi.fn(),
    getRepoByFullName: vi.fn(), createEnvironment: vi.fn(), updateEnvironment: vi.fn(),
  };
});

const {ApiError, getEnvironmentPaths, getEnvironments, getRepository} = Api;

const config: EnvironmentsPageConfig = {apiBase: '/api/deployments/v1', appSubUrl: '', token: '', name: ''};

function env(over: Partial<Environment> = {}): Environment {
  return {
    id: 1, repo_id: 5, name: 'staging', sort_order: 10,
    review_policy: 'none', required_reviewers: 1,
    depends_on: [], require_prior_deployment: false, releases_only: false,
    admins_can_bypass: true, restrict_reviewers: false, reviewer_user_ids: [], reviewer_team_ids: [],
    auto_promote: false, wait_minutes: 0, deploy_window: null, required_status_contexts: [], exclusive_lock: false,
    created_unix: 0, updated_unix: 0, can_write: true,
    ...over,
  };
}

afterEach(() => {
  vi.clearAllMocks();
});

test('two rows from the API render two rows and the new-environment form', async () => {
  vi.mocked(getEnvironments).mockResolvedValue([env({id: 1, name: 'a'}), env({id: 2, name: 'b'})]);
  vi.mocked(getEnvironmentPaths).mockResolvedValue({nodes: [], edges: []});
  vi.mocked(getRepository).mockResolvedValue(null);

  const root = document.createElement('div');
  createApp(EnvironmentsPage, {config}).mount(root);

  await vi.waitFor(() => expect(root.querySelectorAll('#deployments-environments-body tr')).toHaveLength(2));
  expect(root.querySelector('#deployments-new-environment')!.classList.contains('tw-hidden')).toBe(false);
  expect(root.querySelector('#deployments-error')).toBeNull();
});

test('a rejected read renders the banner and no new-environment form', async () => {
  vi.mocked(getEnvironments).mockRejectedValue(new ApiError('boom', 500, '', ''));

  const root = document.createElement('div');
  createApp(EnvironmentsPage, {config}).mount(root);

  await vi.waitFor(() => expect(root.querySelector('#deployments-error')).not.toBeNull());
  expect(root.querySelector('#deployments-new-environment')!.classList.contains('tw-hidden')).toBe(true);
});
