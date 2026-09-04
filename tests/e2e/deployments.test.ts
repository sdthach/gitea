import {env} from 'node:process';
import {test, expect} from '@playwright/test';
import type {APIRequestContext, Page} from '@playwright/test';
import {login, loginUser, apiCreateRepo, apiCreateUser, apiDeleteUser, apiHeaders, baseUrl, randomString} from './utils.ts';

// The deployments pages authenticate with a token, not the browser session, and mint one per
// page render. These tests are what proves that: a signed-in user reaches the data without
// ever being shown the token prompt, which is the failure the prompt exists to recover from.

async function apiRepoID(request: APIRequestContext, owner: string, name: string): Promise<number> {
  const response = await request.get(`${baseUrl()}/api/v1/repos/${owner}/${name}`, {headers: apiHeaders()});
  expect(response.ok()).toBe(true);
  return (await response.json()).id;
}

// A fresh instance seeds no environment — the names are the operator's — so a test that
// needs one creates it, over the endpoint an operator would use.
async function apiCreateEnvironment(request: APIRequestContext, repoID: number, name: string, sortOrder = 10): Promise<number> {
  const response = await request.post(`${baseUrl()}/api/deployments/v1/environments`, {
    headers: apiHeaders(),
    data: {repo_id: repoID, name, sort_order: sortOrder, review_policy: 'none', required_reviewers: 1},
  });
  expect(response.ok(), `create environment ${name}: ${await response.text()}`).toBe(true);
  return (await response.json()).id;
}

async function apiCreateRelease(request: APIRequestContext, owner: string, name: string, tag: string) {
  const response = await request.post(`${baseUrl()}/api/v1/repos/${owner}/${name}/releases`, {
    headers: apiHeaders(),
    data: {tag_name: tag, target_commitish: 'main', name: tag},
  });
  expect(response.ok(), `create release ${tag}: ${await response.text()}`).toBe(true);
}

test('deployments matrix loads its rows for a signed-in user', async ({page}) => {
  const repoName = `e2e-deployments-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;
  const tag = 'release-v1.0.0';

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  await apiCreateEnvironment(page.request, repoID, 'sandbox', 50);
  await apiCreateEnvironment(page.request, repoID, 'live', 10);
  await apiCreateRelease(page.request, owner, repoName, tag);

  // A second repository declaring only the later column, with the newer release, so it is
  // read first: the union then meets sandbox before live, and only sort_order reorders them.
  const otherName = `e2e-deployments-${randomString(8)}`;
  await apiCreateRepo(page.request, {name: otherName});
  const otherID = await apiRepoID(page.request, owner, otherName);
  await apiCreateEnvironment(page.request, otherID, 'sandbox', 50);
  await apiCreateRelease(page.request, owner, otherName, tag);

  await page.goto('/deployments');

  // The page mints its own token, so the prompt — the recovery path for a page that could
  // not authenticate — must never appear to a signed-in user.
  await expect(page.locator('#deployments-token-box')).toBeHidden();
  await expect(page.locator('#deployments-error')).toBeHidden();
  await expect(page.locator('#deployments-grid-body')).toContainText(tag);
  await expect(page.locator('#deployments-grid-head')).toContainText('sandbox');

  // Columns follow the configured order, not the order the repositories were read in.
  await expect(page.locator('#deployments-grid-head th', {hasText: 'live'})).toBeVisible();
  const columns = await page.locator('#deployments-grid-head th').allTextContents();
  expect(columns.indexOf('live')).toBeLessThan(columns.indexOf('sandbox'));
});

test('deployments new page plans a deploy and offers to confirm it', async ({page}) => {
  const repoName = `e2e-deployments-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;
  const tag = 'release-v1.0.0';

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  await apiCreateEnvironment(page.request, repoID, 'promote-target');
  await apiCreateRelease(page.request, owner, repoName, tag);

  await page.goto(`/deployments/new?repo=${owner}/${repoName}&environment=promote-target&release_tag=${tag}`);

  await expect(page.locator('#deployments-token-box')).toBeHidden();
  await expect(page.locator('#deployments-plan')).toContainText(tag);
  await expect(page.locator('#deployments-plan')).toContainText('promote-target');

  // The plan is the first of the confirm step's two halves: nothing is dispatched until the
  // button it enables is pressed.
  const confirm = page.locator('#deployments-confirm');
  await expect(confirm).toBeEnabled();
  await confirm.click();

  // With no runner attached the dispatch may fail; what must never happen is a press that
  // reports nothing. Either the deploy is recorded or the refusal states its next action.
  await expect(page.locator('#deployments-done:visible, #deployments-error:visible')).toHaveCount(1);
});

test('deployments environment editor creates an environment and gates it', async ({page}) => {
  const repoName = `e2e-deployments-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;
  const tag = 'release-v1.0.0';

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  await apiCreateRelease(page.request, owner, repoName, tag);

  await page.goto('/deployments/environments');
  await expect(page.locator('#deployments-token-box')).toBeHidden();

  await page.getByLabel('Environment name').fill('staging');
  await page.getByLabel('Repository').fill(`${owner}/${repoName}`);
  await page.getByRole('button', {name: 'New environment'}).click();

  // A name-only create lands on the detail page with nothing gating a deploy yet.
  await expect(page).toHaveURL(/\/deployments\/environments\/\d+\/edit$/);
  await expect(page.locator('#deployments-heading')).toContainText('staging');
  await expect(page.locator('#deployments-checks')).toContainText('Nothing gates a deploy here');
  await expect(page.locator('#deployments-token-box')).toBeHidden();

  // Sequence needs somewhere to have been first, and this scope holds one environment.
  await page.getByLabel('Add check').selectOption('sequence');
  await expect(page.locator('[data-check="sequence"]')).toContainText('No other environment shares this scope');

  await page.getByLabel('Add check').selectOption('release_kind');
  const releaseKind = page.locator('[data-check="release_kind"]');
  await releaseKind.getByRole('button', {name: 'Save'}).click();
  await expect(releaseKind).toContainText('prereleases are refused here');

  await page.reload();
  await expect(page.locator('[data-check="release_kind"]')).toContainText('prereleases are refused here');
  await expect(page.locator('#deployments-token-box')).toBeHidden();

  // Removing a check must reset its fields, because PUT replaces the whole row.
  await page.getByLabel('Add check').selectOption('reviews');
  const reviews = page.locator('[data-check="reviews"]');
  await reviews.getByRole('button', {name: 'Save'}).click();
  await expect(reviews).toContainText('1 review');
  await reviews.getByRole('button', {name: 'Remove'}).click();
  await expect(reviews).toHaveCount(0);

  const environmentID = /environments\/(\d+)\/edit/.exec(page.url())![1];
  const row = await page.request.get(`${baseUrl()}/api/deployments/v1/environments/${environmentID}`, {headers: apiHeaders()});
  expect(row.ok(), `read environment ${environmentID}: ${await row.text()}`).toBe(true);
  const stored = await row.json();
  expect(stored.review_policy).toBe('none');
  expect(stored.required_reviewers).toBe(1);

  await page.goto('/deployments/environments');
  await expect(page.locator('tr', {hasText: `${owner}/${repoName}`})).toContainText('Release kind');
  await expect(page.locator('#deployments-token-box')).toBeHidden();

  await page.goto('/deployments');
  await expect(page.locator('#deployments-grid-head')).toContainText('staging');
  await expect(page.locator('#deployments-token-box')).toBeHidden();
});

test('deployments environment detail offers a reader no control', async ({page}) => {
  const reader = `e2ereader${randomString(8)}`;
  const envName = `readonly-${randomString(6)}`;

  // An instance-wide environment is writable by site administrators alone, so any other
  // signed-in account reaches the detail page as a reader.
  const environmentID = await apiCreateEnvironment(page.request, 0, envName);
  await apiCreateUser(page.request, reader);

  try {
    await loginUser(page, reader);
    await page.goto(`/deployments/environments/${environmentID}/edit`);

    const detail = page.locator('#deployments-detail');
    await expect(page.locator('#deployments-heading')).toContainText(envName);
    for (const name of ['Save', 'Remove', 'Edit', 'Delete this environment', 'Bind a secret', 'Unbind']) {
      await expect(detail.getByRole('button', {name})).toHaveCount(0);
    }
    await expect(detail.getByLabel('Add check')).toHaveCount(0);
    await expect(page.locator('#deployments-name')).toHaveJSProperty('readOnly', true);
    await expect(page.locator('#deployments-danger')).toBeHidden();
    await expect(page.locator('#deployments-token-box')).toBeHidden();
    await expect(page.locator('#deployments-error')).toBeHidden();
  } finally {
    await apiDeleteUser(page.request, reader);
  }
});

// dragNode moves the mouse to from's center, presses, steps to to's center and releases — a
// real pointer drag, not the HTML5 drag-and-drop gesture PromotionPath.vue does not use. It
// waits for the PUT the drop fires so a caller's next read is never racing the write.
async function dragNode(page: Page, scope: string, from: string, to: string) {
  const fromLocator = page.locator(`${scope} [data-promotion-node="${from}"]`);
  await fromLocator.scrollIntoViewIfNeeded(); // the accumulating list of scopes can push this one off-screen
  const fromBox = await fromLocator.boundingBox();
  const toBox = await page.locator(`${scope} [data-promotion-node="${to}"]`).boundingBox();
  if (!fromBox || !toBox) throw new Error('a promotion path node has no bounding box');
  const [response] = await Promise.all([
    page.waitForResponse((resp) => resp.request().method() === 'PUT' && resp.url().includes('/environments/')),
    (async () => {
      await page.mouse.move(fromBox.x + fromBox.width / 2, fromBox.y + fromBox.height / 2);
      await page.mouse.down();
      await page.mouse.move(toBox.x + toBox.width / 2, toBox.y + toBox.height / 2, {steps: 5});
      await page.mouse.up();
    })(),
  ]);
  return response;
}

test('deployments path editor connects two environments', async ({page}) => {
  const repoName = `e2e-deployments-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  await apiCreateEnvironment(page.request, repoID, 'a');
  await apiCreateEnvironment(page.request, repoID, 'b');

  await page.goto('/deployments/environments');
  const scope = `[data-repo-id="${repoID}"]`;
  await expect(page.locator(`${scope} [data-promotion-node="a"]`)).toBeVisible();
  await expect(page.locator(`${scope} [data-promotion-node="b"]`)).toBeVisible();

  const response = await dragNode(page, scope, 'a', 'b');
  expect(response.ok(), `connect a to b: ${await response.text()}`).toBe(true);

  const graphResp = await page.request.get(`${baseUrl()}/api/deployments/v1/environments/paths?repo_id=${repoID}`, {headers: apiHeaders()});
  expect(graphResp.ok()).toBe(true);
  const graph = await graphResp.json();
  const nodeA = graph.nodes.find((n: {name: string}) => n.name === 'a');
  expect(nodeA.depends_on).toContain('b');
});

test('deployments path editor refuses a cycle', async ({page}) => {
  const repoName = `e2e-deployments-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  const aID = await apiCreateEnvironment(page.request, repoID, 'a');
  await apiCreateEnvironment(page.request, repoID, 'b');

  const setDependency = await page.request.put(`${baseUrl()}/api/deployments/v1/environments/${aID}`, {
    headers: apiHeaders(),
    data: {repo_id: repoID, name: 'a', sort_order: 10, review_policy: 'none', required_reviewers: 1, depends_on: ['b']},
  });
  expect(setDependency.ok(), `set a depends_on b: ${await setDependency.text()}`).toBe(true);

  await page.goto('/deployments/environments');
  const scope = `[data-repo-id="${repoID}"]`;
  await expect(page.locator(`${scope} [data-promotion-node="a"]`)).toBeVisible();
  await expect(page.locator(`${scope} [data-promotion-node="b"]`)).toBeVisible();

  // b onto a would make b depend on a too, closing the cycle a already opened by depending on b.
  const response = await dragNode(page, scope, 'b', 'a');
  expect(response.ok()).toBe(false);

  await expect(page.locator(`${scope} .ui.negative.message`)).toContainText('cycle');

  const graphResp = await page.request.get(`${baseUrl()}/api/deployments/v1/environments/paths?repo_id=${repoID}`, {headers: apiHeaders()});
  const graph = await graphResp.json();
  const nodeB = graph.nodes.find((n: {name: string}) => n.name === 'b');
  expect(nodeB.depends_on).toEqual([]);
});

test('deployments path editor screenshot', async ({page}) => {
  const shotsDir = env.DEPLOYMENTS_SHOTS_DIR;
  test.skip(!shotsDir, 'DEPLOYMENTS_SHOTS_DIR not set'); // eslint-disable-line playwright/no-skipped-test -- conditional skip, the reason is in the message

  const repoName = `e2e-deployments-shots-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  await apiCreateEnvironment(page.request, repoID, 'sandbox', 10);
  await apiCreateEnvironment(page.request, repoID, 'production', 20);

  await page.goto('/deployments/environments');
  const scope = `[data-repo-id="${repoID}"]`;
  await expect(page.locator(`${scope} [data-promotion-node="sandbox"]`)).toBeVisible();
  await page.screenshot({path: `${shotsDir}/environments-path.png`, fullPage: true});
});
