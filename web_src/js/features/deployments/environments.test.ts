import {checks, normalize, payloadOf, scopeLabel} from './environments.ts';
import type {Environment} from './types.ts';

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

test('normalize fills the array fields a freshly-built draft may not carry yet', () => {
  const draft = normalize({...env(), depends_on: undefined as never, reviewer_user_ids: undefined as never});
  expect(draft.depends_on).toEqual([]);
  expect(draft.reviewer_user_ids).toEqual([]);
});

// normalize must copy every array it fills, not hand back the input's own reference: a v-model
// bound into one normalized draft's array (EnvironmentPage.vue's depends_on picker) would
// otherwise mutate a sibling draft normalized from the same row, and the row itself.
test('normalize copies its array fields, so mutating one draft touches neither a sibling draft nor the input', () => {
  const source = env({depends_on: ['live']});
  const first = normalize(source);
  const second = normalize(source);
  first.depends_on[0] = 'mutated';
  expect(second.depends_on).toEqual(['live']);
  expect(source.depends_on).toEqual(['live']);
});

// payloadOf must forward every field the endpoint accepts, including the promotion path
// editor's own fields, or a write from the identity or checks form would silently clear them —
// UpdateEnvironmentHandler replaces the whole row from this body.
test('payloadOf carries the promotion path fields forward unchanged', () => {
  const body = payloadOf(env({auto_promote: true, wait_minutes: 30, exclusive_lock: true, required_status_contexts: ['ci/build']}));
  expect(body.auto_promote).toBe(true);
  expect(body.wait_minutes).toBe(30);
  expect(body.exclusive_lock).toBe(true);
  expect(body.required_status_contexts).toEqual(['ci/build']);
});

test('payloadOf carries depends_on forward unchanged', () => {
  const body = payloadOf(env({depends_on: ['live']}));
  expect(body.depends_on).toEqual(['live']);
});

test('the sequence check is present once depends_on names something', () => {
  const sequence = checks.find((c) => c.key === 'sequence')!;
  expect(sequence.present(env())).toBe(false);
  expect(sequence.present(env({depends_on: ['live']}))).toBe(true);
});

test('removing the sequence check clears depends_on and require_prior_deployment', () => {
  const sequence = checks.find((c) => c.key === 'sequence')!;
  const draft = env({depends_on: ['live'], require_prior_deployment: true});
  sequence.remove(draft);
  expect(draft.depends_on).toEqual([]);
  expect(draft.require_prior_deployment).toBe(false);
});

test('the bypass summary counts users, teams and whether admins may override', () => {
  const bypass = checks.find((c) => c.key === 'bypass')!;
  const summary = bypass.summary(env({reviewer_user_ids: [1, 2], reviewer_team_ids: [3], admins_can_bypass: false}));
  expect(summary).toBe('2 users and 1 team decide; admins cannot override');
});

test('scopeLabel names the instance-wide default set and a repository', () => {
  expect(scopeLabel(0, undefined)).toBe('instance-wide');
  expect(scopeLabel(5, 'octocat/hello')).toBe('octocat/hello');
  expect(scopeLabel(5, undefined)).toBe('repository 5');
});
