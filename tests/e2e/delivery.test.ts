import {env} from 'node:process';
import {test, expect} from '@playwright/test';
import type {APIRequestContext} from '@playwright/test';
import {login, loginUser, apiCreateRepo, apiCreateUser, apiDeleteUser, apiHeaders, baseUrl, randomString} from './utils.ts';

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
async function apiCreateEnvironment(request: APIRequestContext, repoID: number, name: string, sortOrder = 10): Promise<number> {
  const response = await request.post(`${baseUrl()}/api/deployments/v1/environments`, {
    headers: apiHeaders(),
    data: {repo_id: repoID, name, sort_order: sortOrder, approval_policy: 'none', required_approvals: 1},
  });
  expect(response.ok(), `create environment ${name}: ${await response.text()}`).toBe(true);
  return (await response.json()).id;
}

async function apiCreateLabel(request: APIRequestContext, owner: string, name: string, label: string): Promise<number> {
  const response = await request.post(`${baseUrl()}/api/v1/repos/${owner}/${name}/labels`, {
    headers: apiHeaders(),
    data: {name: label, color: '#00aabb'},
  });
  expect(response.ok(), `create label ${label}: ${await response.text()}`).toBe(true);
  return (await response.json()).id;
}

// The chart draws an issue ccpm manages: one carrying an epic: label, with a deadline for its
// end. The epic's own issue carries type:epic beside that label, exactly as epic-sync leaves it.
async function apiCreateManagedIssue(request: APIRequestContext, owner: string, name: string, title: string, labels: number[], due: string): Promise<number> {
  const response = await request.post(`${baseUrl()}/api/v1/repos/${owner}/${name}/issues`, {
    headers: apiHeaders(),
    data: {title, labels, due_date: due},
  });
  expect(response.ok(), `create issue ${title}: ${await response.text()}`).toBe(true);
  return (await response.json()).number;
}

// A reader has to be able to see the repository at all, so it is made public explicitly
// rather than relying on whatever the instance default happens to be.
async function apiMakeRepoPublic(request: APIRequestContext, owner: string, name: string) {
  const response = await request.patch(`${baseUrl()}/api/v1/repos/${owner}/${name}`, {
    headers: apiHeaders(),
    data: {private: false},
  });
  expect(response.ok(), `publish repo ${name}: ${await response.text()}`).toBe(true);
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
  await apiCreateEnvironment(page.request, repoID, 'sandbox', 50);
  await apiCreateEnvironment(page.request, repoID, 'live', 10);
  await apiCreateRelease(page.request, owner, repoName, tag);

  // A second repository declaring only the later column, with the newer release, so it is
  // read first: the union then meets sandbox before live, and only sort_order reorders them.
  const otherName = `e2e-delivery-${randomString(8)}`;
  await apiCreateRepo(page.request, {name: otherName});
  const otherID = await apiRepoID(page.request, owner, otherName);
  await apiCreateEnvironment(page.request, otherID, 'sandbox', 50);
  await apiCreateRelease(page.request, owner, otherName, tag);

  await page.goto('/delivery/grid');

  // The page mints its own token, so the prompt — the recovery path for a page that could
  // not authenticate — must never appear to a signed-in user.
  await expect(page.locator('#delivery-token-box')).toBeHidden();
  await expect(page.locator('#delivery-error')).toBeHidden();
  await expect(page.locator('#delivery-grid-body')).toContainText(tag);
  await expect(page.locator('#delivery-grid-head')).toContainText('sandbox');

  // Columns follow the configured order, not the order the repositories were read in.
  await expect(page.locator('#delivery-grid-head th', {hasText: 'live'})).toBeVisible();
  const columns = await page.locator('#delivery-grid-head th').allTextContents();
  expect(columns.indexOf('live')).toBeLessThan(columns.indexOf('sandbox'));
});

test('delivery promote page plans a deploy and offers to confirm it', async ({page}) => {
  const repoName = `e2e-delivery-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;
  const tag = 'release-v1.0.0';

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  await apiCreateEnvironment(page.request, repoID, 'promote-target');
  await apiCreateRelease(page.request, owner, repoName, tag);

  await page.goto(`/delivery/promote?repo=${owner}/${repoName}&environment=promote-target&release_tag=${tag}`);

  await expect(page.locator('#delivery-token-box')).toBeHidden();
  await expect(page.locator('#delivery-plan')).toContainText(tag);
  await expect(page.locator('#delivery-plan')).toContainText('promote-target');

  // The plan is the first of the confirm step's two halves: nothing is dispatched until the
  // button it enables is pressed.
  const confirm = page.locator('#delivery-confirm');
  await expect(confirm).toBeEnabled();
  await confirm.click();

  // With no runner attached the dispatch may fail; what must never happen is a press that
  // reports nothing. Either the deploy is recorded or the refusal states its next action.
  await expect(page.locator('#delivery-done:visible, #delivery-error:visible')).toHaveCount(1);
});

test('delivery environment editor creates an environment and gates it', async ({page}) => {
  const repoName = `e2e-delivery-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;
  const tag = 'release-v1.0.0';

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  await apiCreateRelease(page.request, owner, repoName, tag);

  await page.goto('/delivery/environments');
  await expect(page.locator('#delivery-token-box')).toBeHidden();

  await page.getByLabel('Environment name').fill('staging');
  await page.getByLabel('Repository').fill(`${owner}/${repoName}`);
  await page.getByRole('button', {name: 'New environment'}).click();

  // A name-only create lands on the detail page with nothing gating a deploy yet.
  await expect(page).toHaveURL(/\/delivery\/environments\/\d+\/edit$/);
  await expect(page.locator('#delivery-heading')).toContainText('staging');
  await expect(page.locator('#delivery-checks')).toContainText('Nothing gates a deploy here');
  await expect(page.locator('#delivery-token-box')).toBeHidden();

  // Sequence needs somewhere to have been first, and this scope holds one environment.
  await page.getByLabel('Add check').selectOption('sequence');
  await expect(page.locator('[data-check="sequence"]')).toContainText('No other environment shares this scope');

  await page.getByLabel('Add check').selectOption('release_kind');
  const releaseKind = page.locator('[data-check="release_kind"]');
  await releaseKind.getByRole('button', {name: 'Save'}).click();
  await expect(releaseKind).toContainText('prereleases are refused here');

  await page.reload();
  await expect(page.locator('[data-check="release_kind"]')).toContainText('prereleases are refused here');
  await expect(page.locator('#delivery-token-box')).toBeHidden();

  // Removing a check must reset its fields, because PUT replaces the whole row.
  await page.getByLabel('Add check').selectOption('approvals');
  const approvals = page.locator('[data-check="approvals"]');
  await approvals.getByRole('button', {name: 'Save'}).click();
  await expect(approvals).toContainText('1 approval');
  await approvals.getByRole('button', {name: 'Remove'}).click();
  await expect(approvals).toHaveCount(0);

  const environmentID = /environments\/(\d+)\/edit/.exec(page.url())![1];
  const row = await page.request.get(`${baseUrl()}/api/deployments/v1/environments/${environmentID}`, {headers: apiHeaders()});
  expect(row.ok(), `read environment ${environmentID}: ${await row.text()}`).toBe(true);
  const stored = await row.json();
  expect(stored.approval_policy).toBe('none');
  expect(stored.required_approvals).toBe(1);

  await page.goto('/delivery/environments');
  await expect(page.locator('tr', {hasText: `${owner}/${repoName}`})).toContainText('Release kind');
  await expect(page.locator('#delivery-token-box')).toBeHidden();

  await page.goto('/delivery/grid');
  await expect(page.locator('#delivery-grid-head')).toContainText('staging');
  await expect(page.locator('#delivery-token-box')).toBeHidden();
});

test('delivery environment detail offers a reader no control', async ({page}) => {
  const reader = `e2ereader${randomString(8)}`;
  const envName = `readonly-${randomString(6)}`;

  // An instance-wide environment is writable by site administrators alone, so any other
  // signed-in account reaches the detail page as a reader.
  const environmentID = await apiCreateEnvironment(page.request, 0, envName);
  await apiCreateUser(page.request, reader);

  try {
    await loginUser(page, reader);
    await page.goto(`/delivery/environments/${environmentID}/edit`);

    const detail = page.locator('#delivery-detail');
    await expect(page.locator('#delivery-heading')).toContainText(envName);
    for (const name of ['Save', 'Remove', 'Edit', 'Delete this environment', 'Bind a secret', 'Unbind']) {
      await expect(detail.getByRole('button', {name})).toHaveCount(0);
    }
    await expect(detail.getByLabel('Add check')).toHaveCount(0);
    await expect(page.locator('#delivery-name')).toHaveJSProperty('readOnly', true);
    await expect(page.locator('#delivery-danger')).toBeHidden();
    await expect(page.locator('#delivery-token-box')).toBeHidden();
    await expect(page.locator('#delivery-error')).toBeHidden();
  } finally {
    await apiDeleteUser(page.request, reader);
  }
});

test('delivery timeline reads an epic as a bracket and its children as bars', async ({page}) => {
  const repoName = `e2e-delivery-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  const epicLabel = await apiCreateLabel(page.request, owner, repoName, 'epic:checkout');
  const typeLabel = await apiCreateLabel(page.request, owner, repoName, 'type:epic');

  // The epic declares a window that ends a fortnight before the work filed under it, which is
  // the contradiction the chart is the only place to see.
  const epicNumber = await apiCreateManagedIssue(page.request, owner, repoName, 'checkout epic',
    [epicLabel, typeLabel], '2030-03-11T00:00:00Z');
  await apiCreateManagedIssue(page.request, owner, repoName, 'checkout story one', [epicLabel], '2030-03-20T00:00:00Z');
  await apiCreateManagedIssue(page.request, owner, repoName, 'checkout story two', [epicLabel], '2030-03-25T00:00:00Z');

  await page.goto('/delivery/timeline');
  await expect(page.locator('#delivery-token-box')).toBeHidden();

  await page.getByLabel('Repository id').fill(String(repoID));
  await page.getByLabel('Zoom').selectOption('epic');

  const chart = page.locator('#delivery-timeline-body');
  await expect(chart).toContainText(`epic checkout (#${epicNumber}) ends 14 days before the work filed under it`);
  await expect(chart).toContainText('epic: checkout');
  await expect(chart.locator('tr')).toHaveCount(1);
  await expect(chart.locator('tr.warning')).toHaveCount(1);

  // A bracket's window is derived from its children, so dragging one would edit a projection:
  // it carries no handle even for a writer.
  await expect(chart.locator('.delivery-bracket')).toHaveCount(1);
  await expect(chart.locator('[data-drag]')).toHaveCount(0);

  // The axis is the server's: the page states the unit it was handed and never picks one.
  await expect(page.locator('#delivery-ruler-unit')).toContainText('ruler');

  await page.getByLabel('Zoom').selectOption('issue');
  await expect(chart).toContainText('checkout story two');
  await expect(chart.locator('tr')).toHaveCount(3);
  await expect(chart.locator('[data-drag]')).toHaveCount(3);

  await expect(page.locator('#delivery-token-box')).toBeHidden();
  await expect(page.locator('#delivery-error')).toBeHidden();
});

test('delivery timeline offers a reader no handle and no drop target', async ({page}) => {
  const repoName = `e2e-delivery-${randomString(8)}`;
  const reader = `e2ereader${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  // Every setup call authenticates with the instance token, so the browser session stays
  // free for the reader: signing in over an existing session leaves the first user in place.
  await apiCreateRepo(page.request, {name: repoName});
  await apiMakeRepoPublic(page.request, owner, repoName);
  const repoID = await apiRepoID(page.request, owner, repoName);
  const epicLabel = await apiCreateLabel(page.request, owner, repoName, 'epic:checkout');
  const typeLabel = await apiCreateLabel(page.request, owner, repoName, 'type:epic');
  await apiCreateManagedIssue(page.request, owner, repoName, 'checkout epic',
    [epicLabel, typeLabel], '2030-03-11T00:00:00Z');
  await apiCreateManagedIssue(page.request, owner, repoName, 'checkout story one', [epicLabel], '2030-03-20T00:00:00Z');
  await apiCreateUser(page.request, reader);

  try {
    await loginUser(page, reader);
    await page.goto('/delivery/timeline');
    await page.getByLabel('Repository id').fill(String(repoID));
    await page.getByLabel('Group by').selectOption('type');

    // The reader sees the whole chart and every lane it is grouped into...
    const chart = page.locator('#delivery-timeline-body');
    await expect(chart).toContainText('checkout story one');
    await expect(chart.locator('tr.delivery-lane').first()).toBeVisible();

    // ...and no way to move anything: no bar is a handle and no lane is a drop target.
    await expect(chart.locator('[data-drag]')).toHaveCount(0);
    await expect(chart.locator('tr[data-lane]')).toHaveCount(0);
    await expect(page.locator('#delivery-token-box')).toBeHidden();
    await expect(page.locator('#delivery-error')).toBeHidden();
  } finally {
    await apiDeleteUser(page.request, reader);
  }
});
