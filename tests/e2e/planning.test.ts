import {env} from 'node:process';
import {test, expect} from '@playwright/test';
import type {APIRequestContext} from '@playwright/test';
import {login, loginUser, apiCreateRepo, apiCreateUser, apiDeleteUser, apiHeaders, baseUrl, randomString} from './utils.ts';

// The planning pages authenticate with a token, not the browser session, cached in the
// session store and reused across renders rather than minted fresh each time. These tests
// are what proves that: a signed-in user reaches the data without ever being shown the token
// prompt, which is the failure the prompt exists to recover from.

async function apiRepoID(request: APIRequestContext, owner: string, name: string): Promise<number> {
  const response = await request.get(`${baseUrl()}/api/v1/repos/${owner}/${name}`, {headers: apiHeaders()});
  expect(response.ok()).toBe(true);
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

test('planning roadmap reads an epic as a bracket and its children as bars', async ({page}) => {
  const repoName = `e2e-planning-${randomString(8)}`;
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

  await page.goto('/planning/roadmap');
  await expect(page.locator('#planning-token-box')).toBeHidden();

  await page.getByLabel('Repository id').fill(String(repoID));
  await page.getByLabel('Zoom').selectOption('epic');

  const chart = page.locator('#planning-roadmap-body');
  await expect(chart).toContainText(`epic checkout (#${epicNumber}) ends 14 days before the work filed under it`);
  await expect(chart).toContainText('epic: checkout');
  await expect(chart.locator('tr')).toHaveCount(1);
  await expect(chart.locator('tr.warning')).toHaveCount(1);

  // A bracket's window is derived from its children, so dragging one would edit a projection:
  // it carries no handle even for a writer.
  await expect(chart.locator('.planning-bracket')).toHaveCount(1);
  await expect(chart.locator('[data-drag]')).toHaveCount(0);

  // The axis is the server's: the page states the unit it was handed and never picks one.
  await expect(page.locator('#planning-ruler-unit')).toContainText('ruler');

  await page.getByLabel('Zoom').selectOption('issue');
  await expect(chart).toContainText('checkout story two');
  await expect(chart.locator('tr')).toHaveCount(3);
  await expect(chart.locator('[data-drag]')).toHaveCount(3);

  await expect(page.locator('#planning-token-box')).toBeHidden();
  await expect(page.locator('#planning-error')).toBeHidden();
});

test('planning roadmap offers a reader no handle and no drop target', async ({page}) => {
  const repoName = `e2e-planning-${randomString(8)}`;
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
    await page.goto('/planning/roadmap');
    await page.getByLabel('Repository id').fill(String(repoID));
    await page.getByLabel('Group by').selectOption('type');

    // The reader sees the whole chart and every group it is grouped into...
    const chart = page.locator('#planning-roadmap-body');
    await expect(chart).toContainText('checkout story one');
    await expect(chart.locator('tr.planning-group').first()).toBeVisible();

    // ...and no way to move anything: no bar is a handle and no group is a drop target.
    await expect(chart.locator('[data-drag]')).toHaveCount(0);
    await expect(chart.locator('tr[data-group]')).toHaveCount(0);
    await expect(page.locator('#planning-token-box')).toBeHidden();
    await expect(page.locator('#planning-error')).toBeHidden();
  } finally {
    await apiDeleteUser(page.request, reader);
  }
});
