import {env} from 'node:process';
import {test, expect} from '@playwright/test';
import type {APIRequestContext} from '@playwright/test';
import {login, loginUser, apiCreateIssue, apiCreateRepo, apiCreateUser, apiDeleteUser, apiHeaders, baseUrl, randomString} from './utils.ts';

// The planning pages authenticate with a token, not the browser session, cached in the
// session store and reused across renders rather than minted fresh each time. These tests
// are what proves that: a signed-in user reaches the data without ever being shown the token
// prompt, which is the failure the prompt exists to recover from.

async function apiRepoID(request: APIRequestContext, owner: string, name: string): Promise<number> {
  const response = await request.get(`${baseUrl()}/api/v1/repos/${owner}/${name}`, {headers: apiHeaders()});
  expect(response.ok()).toBe(true);
  return (await response.json()).id;
}

// The chart draws an issue that is managed: one carrying an assigned type, with a deadline for
// its end.
async function apiCreateManagedIssue(request: APIRequestContext, owner: string, name: string, title: string, due: string): Promise<number> {
  const response = await request.post(`${baseUrl()}/api/v1/repos/${owner}/${name}/issues`, {
    headers: apiHeaders(),
    data: {title, due_date: due},
  });
  expect(response.ok(), `create issue ${title}: ${await response.text()}`).toBe(true);
  return (await response.json()).number;
}

async function apiIssueGlobalID(request: APIRequestContext, owner: string, name: string, number: number): Promise<number> {
  const response = await request.get(`${baseUrl()}/api/v1/repos/${owner}/${name}/issues/${number}`, {headers: apiHeaders()});
  expect(response.ok()).toBe(true);
  return (await response.json()).id;
}

// The instance seeds a fixed set of types on first boot (epic, story, task, ...), so a fresh
// e2e repository already has one visible without creating it first.
async function apiIssueTypeID(request: APIRequestContext, repoID: number, name: string): Promise<number> {
  const response = await request.get(`${baseUrl()}/api/planning/v1/issue-types?repo_id=${repoID}`, {headers: apiHeaders()});
  expect(response.ok(), `list issue types: ${await response.text()}`).toBe(true);
  const types = await response.json() as Array<{id: number, name: string}>;
  const found = types.find((t) => t.name === name);
  expect(found, `type ${name} is seeded on every instance`).toBeTruthy();
  return found!.id;
}

async function apiSetIssueType(request: APIRequestContext, owner: string, name: string, issueNumber: number, typeID: number) {
  const issueID = await apiIssueGlobalID(request, owner, name, issueNumber);
  const response = await request.put(`${baseUrl()}/api/planning/v1/issues/${issueID}/type`, {
    headers: apiHeaders(),
    data: {repo: `${owner}/${name}`, type_id: typeID},
  });
  expect(response.ok(), `set issue type: ${await response.text()}`).toBe(true);
}

async function apiSetIssueParent(request: APIRequestContext, owner: string, name: string, childNumber: number, parentNumber: number) {
  const childID = await apiIssueGlobalID(request, owner, name, childNumber);
  const parentID = await apiIssueGlobalID(request, owner, name, parentNumber);
  const response = await request.put(`${baseUrl()}/api/planning/v1/issues/${childID}/parent`, {
    headers: apiHeaders(),
    data: {repo: `${owner}/${name}`, parent_issue_id: parentID},
  });
  expect(response.ok(), `set issue parent: ${await response.text()}`).toBe(true);
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

// template_type "none" starts a project with no columns, so the board's own two columns are
// exactly the ones this test creates, nothing a default template added.
async function apiCreateProject(request: APIRequestContext, owner: string, name: string, title: string): Promise<number> {
  const response = await request.post(`${baseUrl()}/api/v1/repos/${owner}/${name}/projects`, {
    headers: apiHeaders(),
    data: {title, template_type: 'none'},
  });
  expect(response.ok(), `create project: ${await response.text()}`).toBe(true);
  return (await response.json()).id;
}

async function apiCreateProjectColumn(request: APIRequestContext, owner: string, name: string, projectID: number, title: string): Promise<number> {
  const response = await request.post(`${baseUrl()}/api/v1/repos/${owner}/${name}/projects/${projectID}/columns`, {
    headers: apiHeaders(),
    data: {title},
  });
  expect(response.ok(), `create project column: ${await response.text()}`).toBe(true);
  return (await response.json()).id;
}

async function apiMoveCardToColumn(request: APIRequestContext, issueID: number, repo: string, projectID: number, columnID: number) {
  const response = await request.post(`${baseUrl()}/api/planning/v1/board/cards/${issueID}/column`, {
    headers: apiHeaders(),
    data: {repo, project_id: projectID, column_id: columnID},
  });
  expect(response.ok(), `move card to column: ${await response.text()}`).toBe(true);
}

async function apiBoardCardColumn(request: APIRequestContext, repoID: number, projectID: number, issueID: number): Promise<number> {
  const response = await request.get(`${baseUrl()}/api/planning/v1/board?repo_id=${repoID}&project_id=${projectID}`, {headers: apiHeaders()});
  expect(response.ok(), `get board: ${await response.text()}`).toBe(true);
  const board = await response.json() as {groups: Array<{columns: Array<{cards: Array<{issue_id: number, column_id: number}>}>}>};
  const card = board.groups.flatMap((g) => g.columns).flatMap((c) => c.cards).find((c) => c.issue_id === issueID);
  expect(card, `issue ${issueID} is a card on the board`).toBeTruthy();
  return card!.column_id;
}

async function apiAssignIssue(request: APIRequestContext, owner: string, name: string, issueNumber: number, assignee: string) {
  const response = await request.patch(`${baseUrl()}/api/v1/repos/${owner}/${name}/issues/${issueNumber}`, {
    headers: apiHeaders(),
    data: {assignees: [assignee]},
  });
  expect(response.ok(), `assign issue: ${await response.text()}`).toBe(true);
}

// apiBoardColumnOrder reads one column's own cards, in their published order, across every
// group — the same shape an order write covers, so a test can tell a real reorder from a card
// silently dropped out of the column by a regression in that cross-group merge.
async function apiBoardColumnOrder(request: APIRequestContext, repoID: number, projectID: number, groupBy: string, columnID: number): Promise<number[]> {
  const response = await request.get(`${baseUrl()}/api/planning/v1/board?repo_id=${repoID}&project_id=${projectID}&group_by=${groupBy}`, {headers: apiHeaders()});
  expect(response.ok(), `get board: ${await response.text()}`).toBe(true);
  const board = await response.json() as {groups: Array<{columns: Array<{column_id: number, cards: Array<{issue_id: number}>}>}>};
  return board.groups
    .flatMap((g) => g.columns)
    .filter((c) => c.column_id === columnID)
    .flatMap((c) => c.cards)
    .map((c) => c.issue_id);
}

test('planning roadmap reads a parent as a bracket and its children as bars', async ({page}) => {
  const repoName = `e2e-planning-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  const epicTypeID = await apiIssueTypeID(page.request, repoID, 'epic');
  const storyTypeID = await apiIssueTypeID(page.request, repoID, 'story');

  // The parent declares a window that ends a fortnight before the work filed under it, which
  // is the contradiction the chart is the only place to see.
  const epicNumber = await apiCreateManagedIssue(page.request, owner, repoName, 'checkout epic', '2030-03-11T00:00:00Z');
  const storyOne = await apiCreateManagedIssue(page.request, owner, repoName, 'checkout story one', '2030-03-20T00:00:00Z');
  const storyTwo = await apiCreateManagedIssue(page.request, owner, repoName, 'checkout story two', '2030-03-25T00:00:00Z');
  await apiSetIssueType(page.request, owner, repoName, epicNumber, epicTypeID);
  await apiSetIssueType(page.request, owner, repoName, storyOne, storyTypeID);
  await apiSetIssueType(page.request, owner, repoName, storyTwo, storyTypeID);
  await apiSetIssueParent(page.request, owner, repoName, storyOne, epicNumber);
  await apiSetIssueParent(page.request, owner, repoName, storyTwo, epicNumber);

  await page.goto('/planning/roadmap');
  await expect(page.locator('#planning-token-box')).toBeHidden();

  await page.getByLabel('Repository id').fill(String(repoID));
  await page.getByLabel('Zoom').selectOption('parent');

  const chart = page.locator('#planning-roadmap-body');
  await expect(chart).toContainText(`parent "checkout epic" (#${epicNumber}) ends 14 days before the work filed under it`);
  await expect(chart).toContainText('parent: checkout epic');
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
  const epicTypeID = await apiIssueTypeID(page.request, repoID, 'epic');
  const storyTypeID = await apiIssueTypeID(page.request, repoID, 'story');
  const epicNumber = await apiCreateManagedIssue(page.request, owner, repoName, 'checkout epic', '2030-03-11T00:00:00Z');
  const storyOne = await apiCreateManagedIssue(page.request, owner, repoName, 'checkout story one', '2030-03-20T00:00:00Z');
  await apiSetIssueType(page.request, owner, repoName, epicNumber, epicTypeID);
  await apiSetIssueType(page.request, owner, repoName, storyOne, storyTypeID);
  await apiSetIssueParent(page.request, owner, repoName, storyOne, epicNumber);
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

test('planning board drags a card between columns', async ({page}) => {
  const repoName = `e2e-planning-board-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;
  const repo = `${owner}/${repoName}`;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  const projectID = await apiCreateProject(page.request, owner, repoName, 'Board e2e');
  const columnOne = await apiCreateProjectColumn(page.request, owner, repoName, projectID, 'Todo');
  const columnTwo = await apiCreateProjectColumn(page.request, owner, repoName, projectID, 'Doing');

  const cardOne = await apiCreateIssue(page.request, {owner, repo: repoName, title: 'card one', projects: [projectID]});
  const cardTwo = await apiCreateIssue(page.request, {owner, repo: repoName, title: 'card two', projects: [projectID]});
  const cardOneID = await apiIssueGlobalID(page.request, owner, repoName, cardOne.index);
  const cardTwoID = await apiIssueGlobalID(page.request, owner, repoName, cardTwo.index);
  await apiMoveCardToColumn(page.request, cardOneID, repo, projectID, columnOne);
  await apiMoveCardToColumn(page.request, cardTwoID, repo, projectID, columnOne);

  await page.goto(`/planning/projects/${owner}/${repoName}/${projectID}?view=board`);
  await expect(page.getByText('Todo')).toBeVisible();
  await expect(page.getByText('Doing')).toBeVisible();

  const sourceCard = page.locator('.board-card').filter({hasText: 'card one'});
  const targetCell = page.locator(`[data-column-id="${columnTwo}"]`);
  const from = (await sourceCard.boundingBox())!;
  const to = (await targetCell.boundingBox())!;
  const startX = from.x + from.width / 2;
  const startY = from.y + from.height / 2;
  const endX = to.x + to.width / 2;
  const endY = to.y + to.height / 2;

  await page.mouse.move(startX, startY);
  await page.mouse.down();
  // sortablejs's own delay elapses and it marks the item chosen before any drag is recognised.
  await expect(sourceCard).toHaveClass(/tw-cursor-grabbing/);
  for (let step = 1; step <= 20; step++) {
    await page.mouse.move(startX + ((endX - startX) * step) / 20, startY + ((endY - startY) * step) / 20);
  }
  await page.mouse.up();

  await expect.poll(() => apiBoardCardColumn(page.request, repoID, projectID, cardOneID)).toBe(columnTwo);
});

test('planning board drags an unassigned card into an assignee swimlane within one column', async ({page}) => {
  const repoName = `e2e-planning-board-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;
  const repo = `${owner}/${repoName}`;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  const projectID = await apiCreateProject(page.request, owner, repoName, 'Board assignee e2e');
  const columnOne = await apiCreateProjectColumn(page.request, owner, repoName, projectID, 'Todo');

  const assigned = await apiCreateIssue(page.request, {owner, repo: repoName, title: 'assigned card', projects: [projectID]});
  const unassigned = await apiCreateIssue(page.request, {owner, repo: repoName, title: 'unassigned card', projects: [projectID]});
  const assignedID = await apiIssueGlobalID(page.request, owner, repoName, assigned.index);
  const unassignedID = await apiIssueGlobalID(page.request, owner, repoName, unassigned.index);
  await apiAssignIssue(page.request, owner, repoName, assigned.index, owner);
  await apiMoveCardToColumn(page.request, assignedID, repo, projectID, columnOne);
  await apiMoveCardToColumn(page.request, unassignedID, repo, projectID, columnOne);

  const before = await apiBoardColumnOrder(page.request, repoID, projectID, 'assignee', columnOne);

  await page.goto(`/planning/projects/${owner}/${repoName}/${projectID}?view=board&group_by=assignee`);
  await expect(page.getByText('unassigned card')).toBeVisible();
  await expect(page.getByText('assigned card', {exact: true})).toBeVisible();

  const sourceCard = page.locator('.board-card').filter({hasText: 'unassigned card'});
  // Both swimlanes carry the same column, stacked one above the other, so the drop target is
  // the same column at a different row rather than a different column.
  const targetCell = page.locator(`[data-column-id="${columnOne}"][data-group-key="${owner}"]`);
  const from = (await sourceCard.boundingBox())!;
  const to = (await targetCell.boundingBox())!;
  const startX = from.x + from.width / 2;
  const startY = from.y + from.height / 2;
  const endX = to.x + to.width / 2;
  const endY = to.y + to.height / 2;

  await page.mouse.move(startX, startY);
  await page.mouse.down();
  await expect(sourceCard).toHaveClass(/tw-cursor-grabbing/);
  for (let step = 1; step <= 20; step++) {
    await page.mouse.move(startX + ((endX - startX) * step) / 20, startY + ((endY - startY) * step) / 20);
  }
  await page.mouse.up();

  await expect.poll(() => apiBoardCardColumn(page.request, repoID, projectID, unassignedID)).toBe(columnOne);
  await expect.poll(() => apiBoardColumnOrder(page.request, repoID, projectID, 'assignee', columnOne)).not.toEqual(before);
  const after = await apiBoardColumnOrder(page.request, repoID, projectID, 'assignee', columnOne);
  expect(after, 'neither card is dropped from the column by the cross-group merge').toHaveLength(2);
  expect(after).toEqual(expect.arrayContaining([assignedID, unassignedID]));
  await expect(page.locator('.ui.negative.message')).toHaveCount(0);
});

test('planning board offers a reader no drag handle', async ({page}) => {
  const repoName = `e2e-planning-board-${randomString(8)}`;
  const reader = `e2ereader${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  // Every setup call authenticates with the instance token, so the browser session stays
  // free for the reader.
  await apiCreateRepo(page.request, {name: repoName});
  await apiMakeRepoPublic(page.request, owner, repoName);
  const projectID = await apiCreateProject(page.request, owner, repoName, 'Board reader e2e');
  const columnOne = await apiCreateProjectColumn(page.request, owner, repoName, projectID, 'Todo');
  const card = await apiCreateIssue(page.request, {owner, repo: repoName, title: 'reader card', projects: [projectID]});
  const cardID = await apiIssueGlobalID(page.request, owner, repoName, card.index);
  await apiMoveCardToColumn(page.request, cardID, `${owner}/${repoName}`, projectID, columnOne);
  await apiCreateUser(page.request, reader);

  try {
    await loginUser(page, reader);
    await page.goto(`/planning/projects/${owner}/${repoName}/${projectID}?view=board`);
    await expect(page.getByText('reader card')).toBeVisible();
    await expect(page.locator('[data-drag]')).toHaveCount(0);
  } finally {
    await apiDeleteUser(page.request, reader);
  }
});
