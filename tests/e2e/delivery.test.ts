import {env} from 'node:process';
import {test, expect} from '@playwright/test';
import type {APIRequestContext} from '@playwright/test';
import {login, apiCreateRepo, apiHeaders, baseUrl, randomString} from './utils.ts';

// The delivery pages authenticate with a token, not the browser session, and mint one per
// page render. These tests are what proves that: a signed-in user reaches the data without
// ever being shown the token prompt, which is the failure the prompt exists to recover from.

async function apiRepoID(request: APIRequestContext, owner: string, name: string): Promise<number> {
  const response = await request.get(`${baseUrl()}/api/v1/repos/${owner}/${name}`, {headers: apiHeaders()});
  expect(response.ok()).toBe(true);
  return (await response.json()).id;
}

// A fresh instance seeds no environment — the names are the operator's — so a test that
// needs one creates it, over the endpoint an operator would use.
async function apiCreateEnvironment(request: APIRequestContext, repoID: number, name: string) {
  const response = await request.post(`${baseUrl()}/api/delivery/v1/environments`, {
    headers: apiHeaders(),
    data: {repo_id: repoID, name, sort_order: 10, approval_policy: 'none', required_approvals: 1},
  });
  expect(response.ok(), `create environment ${name}: ${await response.text()}`).toBe(true);
}

async function apiCreateRelease(request: APIRequestContext, owner: string, name: string, tag: string) {
  const response = await request.post(`${baseUrl()}/api/v1/repos/${owner}/${name}/releases`, {
    headers: apiHeaders(),
    data: {tag_name: tag, target_commitish: 'main', name: tag},
  });
  expect(response.ok(), `create release ${tag}: ${await response.text()}`).toBe(true);
}

test('delivery grid loads its rows for a signed-in user', async ({page}) => {
  const repoName = `e2e-delivery-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;
  const tag = 'release-v1.0.0';

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  await apiCreateEnvironment(page.request, repoID, 'sandbox');
  await apiCreateRelease(page.request, owner, repoName, tag);

  await page.goto('/delivery/grid');

  // The page mints its own token, so the prompt — the recovery path for a page that could
  // not authenticate — must never appear to a signed-in user.
  await expect(page.locator('#delivery-token-box')).toBeHidden();
  await expect(page.locator('#delivery-error')).toBeHidden();
  await expect(page.locator('#delivery-grid-body')).toContainText(tag);
  await expect(page.locator('#delivery-grid-head')).toContainText('sandbox');
});

test('delivery promote page plans a deploy and offers to confirm it', async ({page}) => {
  const repoName = `e2e-delivery-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;
  const tag = 'release-v1.0.0';

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  await apiCreateEnvironment(page.request, repoID, 'sandbox');
  await apiCreateRelease(page.request, owner, repoName, tag);

  await page.goto(`/delivery/promote?repo=${owner}/${repoName}&environment=sandbox&release_tag=${tag}`);

  await expect(page.locator('#delivery-token-box')).toBeHidden();
  await expect(page.locator('#delivery-plan')).toContainText(tag);
  await expect(page.locator('#delivery-plan')).toContainText('sandbox');

  // The plan is the first of the confirm step's two halves: nothing is dispatched until the
  // button the plan enables is pressed.
  const confirm = page.locator('#delivery-confirm');
  await expect(confirm).toBeEnabled();
  await confirm.click();

  // With no runner attached the dispatch may fail; what must never happen is a press that
  // reports nothing. Either the deploy is recorded or the refusal states its next action.
  await expect(page.locator('#delivery-done:visible, #delivery-error:visible')).toHaveCount(1);
});
