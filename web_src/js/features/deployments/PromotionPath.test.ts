import {createApp} from 'vue';
import PromotionPath from './PromotionPath.vue';
import * as Api from './api.ts';
import type {Environment} from './types.ts';

vi.mock('./api.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof Api>();
  return {...actual, getEnvironmentPaths: vi.fn(), getEnvironments: vi.fn(), updateEnvironment: vi.fn()};
});

const {getEnvironmentPaths, getEnvironments, updateEnvironment} = Api;

const config: Api.DeploymentsApiConfig = {apiBase: '/api/deployments/v1', appSubUrl: '', token: 't'};

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

function node(name: string) {
  return {name, depends_on: [], auto_promote: false, checks: {wait_minutes: 0, deploy_window: null, required_status_contexts: [], exclusive_lock: false}};
}

afterEach(() => {
  vi.clearAllMocks();
});

test('a node inherited from the instance defaults renders locked and is never written, while the repository\'s own node writes its own id', async () => {
  vi.mocked(getEnvironmentPaths).mockResolvedValue({nodes: [node('shared'), node('own')], edges: []});
  vi.mocked(getEnvironments).mockImplementation(async (_config, opts) =>
    opts?.repoId === 0 ? [env({id: 100, repo_id: 0, name: 'shared'})] : [env({id: 200, repo_id: 5, name: 'own'})]);
  vi.mocked(updateEnvironment).mockResolvedValue(env());

  const root = document.createElement('div');
  createApp(PromotionPath, {config, repoId: 5, label: 'octocat/hello'}).mount(root);

  await vi.waitFor(() => expect(root.querySelectorAll('[data-promotion-node]')).toHaveLength(2));

  const sharedNode = root.querySelector('[data-promotion-node="shared"]')!;
  const ownNode = root.querySelector('[data-promotion-node="own"]')!;
  expect(sharedNode.classList.contains('deployments-promotion-node-locked')).toBe(true);
  expect(ownNode.classList.contains('deployments-promotion-node-locked')).toBe(false);

  // A change event is dispatched directly, rather than relying on the browser's own
  // click()-on-a-disabled-element suppression, so the assertion is of canWrite's own refusal
  // inside toggleAutoPromote, not merely of the disabled attribute the same rule renders.
  const sharedCheckbox = sharedNode.querySelector('input[type="checkbox"]') as HTMLInputElement;
  expect(sharedCheckbox.disabled).toBe(true);
  sharedCheckbox.dispatchEvent(new Event('change', {bubbles: true}));
  await new Promise((resolve) => setTimeout(resolve, 50));
  expect(updateEnvironment).not.toHaveBeenCalled();

  const ownCheckbox = ownNode.querySelector('input[type="checkbox"]') as HTMLInputElement;
  expect(ownCheckbox.disabled).toBe(false);
  ownCheckbox.dispatchEvent(new Event('change', {bubbles: true}));

  await vi.waitFor(() => expect(updateEnvironment).toHaveBeenCalled());
  expect(vi.mocked(updateEnvironment).mock.calls[0][1]).toBe(200);
});
