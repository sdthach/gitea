import {env} from 'node:process';
import {test, expect} from '@playwright/test';
import type {APIRequestContext} from '@playwright/test';
import {login, loginUser, apiCreateIssue, apiCreateRepo, apiCreateUser, apiDeleteUser, apiHeaders, apiUserHeaders, baseUrl, randomString} from './utils.ts';

// The planning pages authenticate with a token, not the browser session, cached in the
// session store and reused across renders rather than minted fresh each time. These tests
// are what proves that: a signed-in user reaches the data without ever being shown the token
// prompt, which is the failure the prompt exists to recover from.

async function apiRepoID(request: APIRequestContext, owner: string, name: string): Promise<number> {
  const response = await request.get(`${baseUrl()}/api/v1/repos/${owner}/${name}`, {headers: apiHeaders()});
  expect(response.ok()).toBe(true);
  return (await response.json()).id;
}

async function apiUserID(request: APIRequestContext, username: string): Promise<number> {
  const response = await request.get(`${baseUrl()}/api/v1/users/${username}`, {headers: apiHeaders()});
  expect(response.ok(), `get user ${username}: ${await response.text()}`).toBe(true);
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

// apiCreateManagedIssueInProject is apiCreateManagedIssue's own board-ready counterpart: the
// project a board card move requires the issue to already belong to.
async function apiCreateManagedIssueInProject(request: APIRequestContext, owner: string, name: string, title: string, due: string, projectID: number): Promise<number> {
  const response = await request.post(`${baseUrl()}/api/v1/repos/${owner}/${name}/issues`, {
    headers: apiHeaders(),
    data: {title, due_date: due, projects: [projectID]},
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

async function apiSetIssueDates(request: APIRequestContext, owner: string, name: string, issueID: number, start: string, end: string) {
  const response = await request.post(`${baseUrl()}/api/planning/v1/issues/${issueID}/dates`, {
    headers: apiHeaders(),
    data: {repo: `${owner}/${name}`, start, end},
  });
  expect(response.ok(), `set issue dates: ${await response.text()}`).toBe(true);
}

// apiSetIssueEstimate gives an issue remaining work capacity can turn into a heat strip: with no
// estimate at all an assignee's every day reads as zero load, however many issues they carry.
async function apiSetIssueEstimate(request: APIRequestContext, owner: string, name: string, issueID: number, timeEstimate: string) {
  const response = await request.put(`${baseUrl()}/api/planning/v1/issues/${issueID}/estimate`, {
    headers: apiHeaders(),
    data: {repo: `${owner}/${name}`, time_estimate: timeEstimate},
  });
  expect(response.ok(), `set issue estimate: ${await response.text()}`).toBe(true);
}

async function apiRoadmapBar(request: APIRequestContext, repoID: number, issueID: number): Promise<{start_unix: number, end_unix: number}> {
  const response = await request.get(`${baseUrl()}/api/planning/v1/roadmap?repo_id=${repoID}`, {headers: apiHeaders()});
  expect(response.ok(), `get roadmap: ${await response.text()}`).toBe(true);
  const roadmap = await response.json() as {bars: Array<{issue_id: number, start_unix: number, end_unix: number}>};
  const bar = roadmap.bars.find((b) => b.issue_id === issueID);
  expect(bar, `issue ${issueID} has a bar`).toBeTruthy();
  return bar!;
}

async function apiRoadmapArrows(request: APIRequestContext, repoID: number): Promise<Array<{from_issue_id: number, to_issue_id: number}>> {
  const response = await request.get(`${baseUrl()}/api/planning/v1/roadmap?repo_id=${repoID}`, {headers: apiHeaders()});
  expect(response.ok(), `get roadmap: ${await response.text()}`).toBe(true);
  return (await response.json() as {arrows: Array<{from_issue_id: number, to_issue_id: number}>}).arrows;
}

// A fresh repository's Issues unit does not have dependencies turned on: the write endpoint
// refuses dependencies_disabled otherwise.
async function apiEnableDependencies(request: APIRequestContext, owner: string, name: string) {
  const response = await request.patch(`${baseUrl()}/api/v1/repos/${owner}/${name}`, {
    headers: apiHeaders(),
    data: {has_issues: true, internal_tracker: {enable_issue_dependencies: true}},
  });
  expect(response.ok(), `enable dependencies: ${await response.text()}`).toBe(true);
}

// A fresh repository's Issues unit does not have time tracking turned on either: adding a
// tracked-time entry, through the API or through the Time tab's own form, is refused otherwise.
async function apiEnableTimeTracker(request: APIRequestContext, owner: string, name: string) {
  const response = await request.patch(`${baseUrl()}/api/v1/repos/${owner}/${name}`, {
    headers: apiHeaders(),
    data: {has_issues: true, internal_tracker: {enable_time_tracker: true}},
  });
  expect(response.ok(), `enable time tracker: ${await response.text()}`).toBe(true);
}

async function apiAddDependency(request: APIRequestContext, owner: string, name: string, blockedIssueID: number, blockerIssueID: number) {
  const response = await request.post(`${baseUrl()}/api/planning/v1/issues/${blockedIssueID}/dependencies`, {
    headers: apiHeaders(),
    data: {repo: `${owner}/${name}`, depends_on_issue_id: blockerIssueID},
  });
  expect(response.ok(), `add dependency: ${await response.text()}`).toBe(true);
}

// The doer Gitea records is whoever the request's own token belongs to, so a headers
// override lets a caller log time (or run a stopwatch) as a user other than the default
// admin token's own account.
async function apiAddTrackedTime(request: APIRequestContext, owner: string, name: string, issueNumber: number, timeSeconds: number, createdISO: string, headers?: Record<string, string>) {
  const response = await request.post(`${baseUrl()}/api/v1/repos/${owner}/${name}/issues/${issueNumber}/times`, {
    headers: headers || apiHeaders(),
    data: {time: timeSeconds, created: createdISO},
  });
  expect(response.ok(), `add tracked time: ${await response.text()}`).toBe(true);
}

async function apiStartStopwatch(request: APIRequestContext, owner: string, name: string, issueNumber: number, headers?: Record<string, string>) {
  const response = await request.post(`${baseUrl()}/api/v1/repos/${owner}/${name}/issues/${issueNumber}/stopwatch/start`, {headers: headers || apiHeaders()});
  expect(response.ok(), `start stopwatch: ${await response.text()}`).toBe(true);
}

async function apiCreateMilestone(request: APIRequestContext, owner: string, name: string, title: string, dueOn: string): Promise<number> {
  const response = await request.post(`${baseUrl()}/api/v1/repos/${owner}/${name}/milestones`, {
    headers: apiHeaders(),
    data: {title, due_on: dueOn},
  });
  expect(response.ok(), `create milestone: ${await response.text()}`).toBe(true);
  return (await response.json()).id;
}

async function apiSetMilestoneSchedule(request: APIRequestContext, owner: string, name: string, milestoneID: number, start: string) {
  const response = await request.put(`${baseUrl()}/api/planning/v1/milestones/${milestoneID}/schedule`, {
    headers: apiHeaders(),
    data: {repo: `${owner}/${name}`, start},
  });
  expect(response.ok(), `set milestone schedule: ${await response.text()}`).toBe(true);
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

test('planning roadmap offers a reader no handle and no drop target', async ({page}) => {
  const repoName = `e2e-planning-${randomString(8)}`;
  const reader = `e2ereader${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  // Every setup call authenticates with the instance token, so the browser session stays
  // free for the reader: signing in over an existing session leaves the first user in place.
  await apiCreateRepo(page.request, {name: repoName});
  await apiMakeRepoPublic(page.request, owner, repoName);
  const repoID = await apiRepoID(page.request, owner, repoName);
  const projectID = await apiCreateProject(page.request, owner, repoName, 'Roadmap reader e2e');
  const storyTypeID = await apiIssueTypeID(page.request, repoID, 'story');
  const storyOne = await apiCreateManagedIssue(page.request, owner, repoName, 'checkout story one', '2030-03-20T00:00:00Z');
  await apiSetIssueType(page.request, owner, repoName, storyOne, storyTypeID);
  // A due date alone leaves the start inferred, and an inferred-start bar draws only in the
  // "Needs a start" panel, not on the timeline this test means to check for a drag handle on.
  const storyOneID = await apiIssueGlobalID(page.request, owner, repoName, storyOne);
  const today = new Date().toISOString().slice(0, 10);
  await apiSetIssueDates(page.request, owner, repoName, storyOneID, today, today);
  await apiCreateUser(page.request, reader);

  try {
    await loginUser(page, reader);
    await page.goto(`/planning/projects/${owner}/${repoName}/${projectID}?view=roadmap`);

    // The reader sees the chart...
    await expect(page.getByText('checkout story one', {exact: true})).toBeVisible();
    // ...and no way to move anything: no bar is a handle and no drop target.
    await expect(page.locator('[data-drag]')).toHaveCount(0);
    await expect(page.locator('.ui.negative.message')).toHaveCount(0);
  } finally {
    await apiDeleteUser(page.request, reader);
  }
});

test('planning roadmap keys a rollup bracket by kind, and its chevron collapses the parent row', async ({page}) => {
  const repoName = `e2e-planning-roadmap-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  const projectID = await apiCreateProject(page.request, owner, repoName, 'Roadmap bracket e2e');
  const epicTypeID = await apiIssueTypeID(page.request, repoID, 'epic');
  const storyTypeID = await apiIssueTypeID(page.request, repoID, 'story');

  const parentNumber = await apiCreateManagedIssue(page.request, owner, repoName, 'bracket parent', '2030-03-22T00:00:00Z');
  const childNumber = await apiCreateManagedIssue(page.request, owner, repoName, 'bracket child', '2030-03-16T00:00:00Z');
  await apiSetIssueType(page.request, owner, repoName, parentNumber, epicTypeID);
  await apiSetIssueType(page.request, owner, repoName, childNumber, storyTypeID);
  const parentIssueID = await apiIssueGlobalID(page.request, owner, repoName, parentNumber);
  const childIssueID = await apiIssueGlobalID(page.request, owner, repoName, childNumber);
  await apiSetIssueDates(page.request, owner, repoName, parentIssueID, '2030-03-05', '2030-03-22');
  await apiSetIssueDates(page.request, owner, repoName, childIssueID, '2030-03-10', '2030-03-16');
  await apiSetIssueParent(page.request, owner, repoName, childNumber, parentNumber);

  await page.goto(`/planning/projects/${owner}/${repoName}/${projectID}?view=roadmap&group_by=parent&scale=day&at=2030-03-10`);

  const bracket = page.locator('.planning-bracket');
  await expect(bracket).toBeVisible();
  await expect(bracket).toHaveAttribute('title', /bracket parent/);

  const childBar = page.locator('.planning-roadmap-bar').filter({hasText: 'bracket child'});
  await expect(childBar).toBeVisible();

  // The chevron sits on the parent issue's own row, keyed by kind:key the same way the server
  // names a bracket — a mismatched key is the regression this bracket's own visibility guards.
  await page.getByRole('button', {name: 'Collapse'}).click();
  await expect(childBar).toBeHidden();
  await expect(page).toHaveURL(/collapsed=/);
});

test('planning roadmap drags a bar by two days', async ({page}) => {
  const repoName = `e2e-planning-roadmap-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  const projectID = await apiCreateProject(page.request, owner, repoName, 'Roadmap drag e2e');
  const storyTypeID = await apiIssueTypeID(page.request, repoID, 'story');
  const issueNumber = await apiCreateManagedIssue(page.request, owner, repoName, 'drag me', '2030-03-20T00:00:00Z');
  await apiSetIssueType(page.request, owner, repoName, issueNumber, storyTypeID);
  const issueID = await apiIssueGlobalID(page.request, owner, repoName, issueNumber);
  await apiSetIssueDates(page.request, owner, repoName, issueID, '2030-03-10', '2030-03-15');
  const before = await apiRoadmapBar(page.request, repoID, issueID);

  await page.goto(`/planning/projects/${owner}/${repoName}/${projectID}?view=roadmap&scale=day&at=2030-03-10`);
  const outerBar = page.locator('.planning-roadmap-bar').filter({hasText: 'drag me'});
  const bar = page.locator('.planning-roadmap-bar-body').filter({hasText: 'drag me'});
  await expect(bar).toBeVisible();

  const box = (await bar.boundingBox())!;
  const startX = box.x + box.width / 2;
  const startY = box.y + box.height / 2;

  await page.mouse.move(startX, startY);
  await page.mouse.down();
  for (let step = 1; step <= 8; step++) {
    await page.mouse.move(startX + (96 * step) / 8, startY);
  }
  await page.mouse.up();

  // The committed preview reflects the new date immediately, before the write round-trip is
  // even polled for: a bar that reset itself on commit would still read the old start here.
  const expectedStart = new Date((before.start_unix + 172800) * 1000).toISOString().slice(0, 10);
  await expect(outerBar).toHaveAttribute('data-start', expectedStart);

  await expect.poll(async () => (await apiRoadmapBar(page.request, repoID, issueID)).start_unix).toBe(before.start_unix + 172800);
  const after = await apiRoadmapBar(page.request, repoID, issueID);
  expect(after.end_unix - after.start_unix, 'duration is unchanged').toBe(before.end_unix - before.start_unix);
});

test('planning roadmap cancels a drag with Escape, writing nothing', async ({page}) => {
  const repoName = `e2e-planning-roadmap-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  const projectID = await apiCreateProject(page.request, owner, repoName, 'Roadmap escape e2e');
  const storyTypeID = await apiIssueTypeID(page.request, repoID, 'story');
  const issueNumber = await apiCreateManagedIssue(page.request, owner, repoName, 'escape me', '2030-03-20T00:00:00Z');
  await apiSetIssueType(page.request, owner, repoName, issueNumber, storyTypeID);
  const issueID = await apiIssueGlobalID(page.request, owner, repoName, issueNumber);
  await apiSetIssueDates(page.request, owner, repoName, issueID, '2030-03-10', '2030-03-15');
  const before = await apiRoadmapBar(page.request, repoID, issueID);

  await page.goto(`/planning/projects/${owner}/${repoName}/${projectID}?view=roadmap&scale=day&at=2030-03-10`);
  const outerBar = page.locator('.planning-roadmap-bar').filter({hasText: 'escape me'});
  const bar = page.locator('.planning-roadmap-bar-body').filter({hasText: 'escape me'});
  await expect(bar).toBeVisible();

  const box = (await bar.boundingBox())!;
  const startX = box.x + box.width / 2;
  const startY = box.y + box.height / 2;
  const expectedStart = new Date(before.start_unix * 1000).toISOString().slice(0, 10);

  await page.mouse.move(startX, startY);
  await page.mouse.down();
  for (let step = 1; step <= 8; step++) {
    await page.mouse.move(startX + (96 * step) / 8, startY);
  }
  await expect(outerBar).not.toHaveAttribute('data-start', expectedStart);
  await page.keyboard.press('Escape');
  await expect(outerBar).toHaveAttribute('data-start', expectedStart);
  await page.mouse.up();

  const after = await apiRoadmapBar(page.request, repoID, issueID);
  expect(after.start_unix, 'the write the cancelled drag would have made never happened').toBe(before.start_unix);
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

test('planning roadmap draws and removes a dependency arrow', async ({page}) => {
  const repoName = `e2e-planning-arrows-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  const projectID = await apiCreateProject(page.request, owner, repoName, 'Arrows e2e');
  const storyTypeID = await apiIssueTypeID(page.request, repoID, 'story');

  const blockerNumber = await apiCreateManagedIssue(page.request, owner, repoName, 'blocker', '2030-03-15T00:00:00Z');
  const blockedNumber = await apiCreateManagedIssue(page.request, owner, repoName, 'blocked', '2030-03-25T00:00:00Z');
  await apiSetIssueType(page.request, owner, repoName, blockerNumber, storyTypeID);
  await apiSetIssueType(page.request, owner, repoName, blockedNumber, storyTypeID);
  const blockerID = await apiIssueGlobalID(page.request, owner, repoName, blockerNumber);
  const blockedID = await apiIssueGlobalID(page.request, owner, repoName, blockedNumber);
  await apiSetIssueDates(page.request, owner, repoName, blockerID, '2030-03-10', '2030-03-15');
  await apiSetIssueDates(page.request, owner, repoName, blockedID, '2030-03-20', '2030-03-25');
  await apiEnableDependencies(page.request, owner, repoName);
  await apiAddDependency(page.request, owner, repoName, blockedID, blockerID);

  await page.goto(`/planning/projects/${owner}/${repoName}/${projectID}?view=roadmap&scale=day&at=2030-03-10`);
  const arrow = page.locator(`path[data-arrow="${blockerID}>${blockedID}"]`);
  await expect(arrow).toBeVisible();
  // The path is a thin SVG stroke: dispatching straight to the element exercises the click
  // handler without fighting SVG hit-testing over an orthogonal line's bounding box.
  await arrow.dispatchEvent('click');

  await expect.poll(async () => (await apiRoadmapArrows(page.request, repoID)).length).toBe(0);
});

test('planning time view adds and removes an entry', async ({page}) => {
  const repoName = `e2e-planning-time-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  const userID = await apiUserID(page.request, owner);
  const projectID = await apiCreateProject(page.request, owner, repoName, 'Time e2e');
  await apiEnableTimeTracker(page.request, owner, repoName);
  const issueNumber = await apiCreateManagedIssue(page.request, owner, repoName, 'log time on me', '2099-01-01T00:00:00Z');

  // Gitea's own AddTime persists the CURRENT time regardless of a caller-supplied created
  // date (its POST response echoes the request, but the row it stores does not), so the
  // entry this form adds lands on today — the cell this test targets, and the only week
  // navigating with no at= reads.
  const today = new Date().toISOString().slice(0, 10);

  await page.goto(`/planning/projects/${owner}/${repoName}/${projectID}?view=time`);
  const cell = page.locator(`[data-time-cell="${userID}:${today}"]`);
  await cell.click();
  await page.locator('form select').selectOption({label: `log time on me #${issueNumber}`});
  await page.getByPlaceholder('1h 30m').fill('1h 30m');
  await page.getByRole('button', {name: 'Add'}).click();

  await expect(cell).toContainText('1h 30m');
  await expect.poll(async () => {
    const response = await page.request.get(`${baseUrl()}/api/planning/v1/timesheet?repo_id=${repoID}`, {headers: apiHeaders()});
    const sheet = await response.json() as {lanes: Array<{days: Array<{unix: number, seconds: number}>}>};
    return sheet.lanes.flatMap((l) => l.days).reduce((sum, d) => sum + d.seconds, 0);
  }).toBe(5400);

  await cell.getByText('×', {exact: true}).click();
  await expect(cell).not.toContainText('1h 30m');
});

test('planning issue sidebar sets a type then a parent', async ({page}) => {
  const repoName = `e2e-planning-sidebar-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  const typeID = await apiIssueTypeID(page.request, repoID, 'task');
  // SetIssueParent ranks by type: the parent's must outrank the child's, so the parent needs
  // one that outranks task (rank 4) — epic (rank 1) does, and both are instance-seeded.
  const parentTypeID = await apiIssueTypeID(page.request, repoID, 'epic');
  const {index: parentNumber} = await apiCreateIssue(page.request, {owner, repo: repoName, title: 'sidebar parent'});
  const {index: childNumber} = await apiCreateIssue(page.request, {owner, repo: repoName, title: 'sidebar child'});
  const childID = await apiIssueGlobalID(page.request, owner, repoName, childNumber);
  await apiSetIssueType(page.request, owner, repoName, parentNumber, parentTypeID);

  await page.goto(`/${owner}/${repoName}/issues/${childNumber}`);
  const sidebar = page.locator('.issue-sidebar-planning');
  await expect(sidebar).toBeVisible();

  await sidebar.getByLabel('Type').selectOption(String(typeID));
  await sidebar.getByRole('button', {name: 'Save type'}).click();
  await expect(sidebar).toContainText('task');

  await sidebar.getByLabel('Parent issue').fill(String(parentNumber));
  await sidebar.getByRole('button', {name: 'Save parent'}).click();
  await expect(sidebar.getByRole('link', {name: `#${parentNumber} sidebar parent`})).toBeVisible();

  // The sidebar's own DOM and the API it reads from must agree — a sidebar that reads
  // stale or a different endpoint would still "look right" without this check.
  const facetsResponse = await page.request.get(`${baseUrl()}/api/planning/v1/issues/${childID}`, {headers: apiHeaders()});
  const facets = await facetsResponse.json() as {type: {name: string} | null; parent: {number: number} | null};
  expect(facets.type?.name).toBe('task');
  expect(facets.parent?.number).toBe(parentNumber);
});

test('planning milestone form sets a start', async ({page}) => {
  const repoName = `e2e-planning-milestone-form-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  const milestoneID = await apiCreateMilestone(page.request, owner, repoName, 'sidebar sprint', '2030-06-01T00:00:00Z');

  await page.goto(`/${owner}/${repoName}/milestones/${milestoneID}/edit`);
  const field = page.locator('[data-global-init="initPlanningMilestoneStart"]');
  await expect(field).toBeVisible();
  await field.getByLabel('Start date').fill('2030-05-01');
  await field.getByRole('button', {name: 'Save start'}).click();

  await expect.poll(async () => {
    const response = await page.request.get(`${baseUrl()}/api/planning/v1/roadmap?repo_id=${repoID}`, {headers: apiHeaders()});
    const roadmap = await response.json() as {milestones?: Array<{milestone_id: number, start_unix: number}>};
    return roadmap.milestones?.find((m) => m.milestone_id === milestoneID)?.start_unix;
  }).toBe(Math.floor(Date.UTC(2030, 4, 1) / 1000));
});

test('planning settings creates a type and a field', async ({page}) => {
  const repoName = `e2e-planning-settings-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);

  await page.goto(`/planning/settings/${owner}/${repoName}`);
  await page.getByLabel('Name').fill('gadget');
  await page.getByLabel('Color').fill('#123456');
  await page.getByLabel('Icon').fill('octicon-gear');
  await page.getByRole('button', {name: 'Create type'}).click();
  await expect(page.getByRole('cell', {name: 'gadget'})).toBeVisible();

  await page.locator('.ui.secondary.pointing.menu').getByText('fields', {exact: true}).click();
  await page.getByLabel('Key').fill('points');
  await page.getByLabel('Label').fill('Points');
  await page.getByRole('button', {name: 'Create field'}).click();
  await expect(page.getByRole('cell', {name: 'points', exact: true})).toBeVisible();

  const response = await page.request.get(`${baseUrl()}/api/planning/v1/issue-types?repo_id=${repoID}`, {headers: apiHeaders()});
  const types = await response.json() as Array<{name: string}>;
  expect(types.some((t) => t.name === 'gadget'), 'the created type is readable over the API').toBe(true);
});

test('planning pages screenshot', async ({page}) => {
  const shotsDir = env.PLANNING_SHOTS_DIR;
  test.skip(!shotsDir, 'PLANNING_SHOTS_DIR not set'); // eslint-disable-line playwright/no-skipped-test -- conditional skip, the reason is in the message

  const repoName = `e2e-planning-shots-${randomString(8)}`;
  const owner = `e2eshots${randomString(8)}`;

  await apiCreateUser(page.request, owner);
  const headers = apiUserHeaders(owner);
  await apiCreateRepo(page.request, {name: repoName, headers});
  const repoID = await apiRepoID(page.request, owner, repoName);
  const projectID = await apiCreateProject(page.request, owner, repoName, 'Shots project');
  const columns = [
    await apiCreateProjectColumn(page.request, owner, repoName, projectID, 'Todo'),
    await apiCreateProjectColumn(page.request, owner, repoName, projectID, 'Doing'),
    await apiCreateProjectColumn(page.request, owner, repoName, projectID, 'Done'),
  ];
  const storyTypeID = await apiIssueTypeID(page.request, repoID, 'story');
  const epicTypeID = await apiIssueTypeID(page.request, repoID, 'epic');

  const numbers: number[] = [];
  for (let i = 0; i < 6; i++) {
    const day = 10 + i;
    const number = await apiCreateManagedIssueInProject(page.request, owner, repoName, `shot issue ${i + 1}`, `2030-04-${day}T00:00:00Z`, projectID);
    await apiSetIssueType(page.request, owner, repoName, number, storyTypeID);
    const issueID = await apiIssueGlobalID(page.request, owner, repoName, number);
    await apiSetIssueDates(page.request, owner, repoName, issueID, `2030-04-0${i + 1}`, `2030-04-${day}`);
    await apiMoveCardToColumn(page.request, issueID, `${owner}/${repoName}`, projectID, columns[i % columns.length]);
    numbers.push(number);
  }
  for (const number of numbers.slice(0, 3)) {
    await apiAssignIssue(page.request, owner, repoName, number, owner);
    // A heat strip needs load: an assignee with no estimated work reads as zero every day.
    const issueID = await apiIssueGlobalID(page.request, owner, repoName, number);
    await apiSetIssueEstimate(page.request, owner, repoName, issueID, '8h');
  }

  const parentNumber = await apiCreateManagedIssueInProject(page.request, owner, repoName, 'shot parent epic', '2030-05-01T00:00:00Z', projectID);
  await apiSetIssueType(page.request, owner, repoName, parentNumber, epicTypeID);
  await apiSetIssueParent(page.request, owner, repoName, numbers[0], parentNumber);
  await apiSetIssueParent(page.request, owner, repoName, numbers[1], parentNumber);
  const parentIssueID = await apiIssueGlobalID(page.request, owner, repoName, parentNumber);
  await apiSetIssueDates(page.request, owner, repoName, parentIssueID, '2030-04-01', '2030-04-20');
  await apiMoveCardToColumn(page.request, parentIssueID, `${owner}/${repoName}`, projectID, columns[0]);

  const milestoneID = await apiCreateMilestone(page.request, owner, repoName, 'Shot sprint', '2030-04-20T00:00:00Z');
  await apiSetMilestoneSchedule(page.request, owner, repoName, milestoneID, '2030-04-06');

  // One dependency arrow, between two bars already visible in the day-scale window.
  const blockerID = await apiIssueGlobalID(page.request, owner, repoName, numbers[3]);
  const blockedID = await apiIssueGlobalID(page.request, owner, repoName, numbers[4]);
  await apiEnableDependencies(page.request, owner, repoName);
  await apiAddDependency(page.request, owner, repoName, blockedID, blockerID);

  // Two tracked-time entries and a running stopwatch. Gitea's own AddTime persists the
  // CURRENT time regardless of a caller-supplied created date — its echoed POST response
  // reflects the request, but the row it actually stores does not — so these land on
  // today, not on the 2030 window every other shot lives in; the Time tab's own shot
  // therefore reads the current week rather than at=2030-04-08.
  await apiEnableTimeTracker(page.request, owner, repoName);
  await apiAddTrackedTime(page.request, owner, repoName, numbers[3], 3600, '2030-04-08T00:00:00Z', headers);
  await apiAddTrackedTime(page.request, owner, repoName, numbers[3], 5400, '2030-04-09T00:00:00Z', headers);
  await apiStartStopwatch(page.request, owner, repoName, numbers[4], headers);

  await loginUser(page, owner);
  const base = `/planning/projects/${owner}/${repoName}/${projectID}`;
  // Every bar lives around 2030-04, so at= centers each roadmap shot's window on that range —
  // the default (today's date) window would show none of them.
  const shots: Array<[string, string]> = [
    ['table.png', `${base}?view=table`],
    ['board-none.png', `${base}?view=board`],
    ['board-assignee.png', `${base}?view=board&group_by=assignee`],
    ['roadmap-day.png', `${base}?view=roadmap&scale=day&at=2030-04-10`],
    ['roadmap-arrows.png', `${base}?view=roadmap&scale=day&at=2030-04-10`],
    ['time.png', `${base}?view=time`],
    ['roadmap-month.png', `${base}?view=roadmap&scale=month&at=2030-04-10`],
    ['roadmap-parent.png', `${base}?view=roadmap&group_by=parent&at=2030-04-10`],
  ];
  for (const [file, url] of shots) {
    await page.goto(url);
    await page.screenshot({path: `${shotsDir}/${file}`, fullPage: true});
  }

  await page.goto(`/planning/settings/${owner}/${repoName}`);
  await expect(page.getByRole('cell', {name: 'story'})).toBeVisible();
  await page.screenshot({path: `${shotsDir}/settings-types.png`, fullPage: true});
});

// One browser is enough for a static screenshot; running it on both would just double the
// wait for two identical files.
test('planning issue and milestone screenshots', async ({page}, testInfo) => {
  const shotsDir = env.PLANNING_SHOTS_DIR;
  test.skip(!shotsDir, 'PLANNING_SHOTS_DIR not set'); // eslint-disable-line playwright/no-skipped-test -- conditional skip, the reason is in the message
  test.skip(testInfo.project.name !== 'firefox', 'isolated on firefox, see the comment above'); // eslint-disable-line playwright/no-skipped-test -- conditional skip, the reason is in the message

  const repoName = `e2e-planning-sidebar-shots-${randomString(8)}`;
  const owner = env.GITEA_TEST_E2E_USER;

  await login(page);
  await apiCreateRepo(page.request, {name: repoName});
  const repoID = await apiRepoID(page.request, owner, repoName);
  const typeID = await apiIssueTypeID(page.request, repoID, 'task');
  const parentTypeID = await apiIssueTypeID(page.request, repoID, 'epic'); // must outrank the child's type
  const {index: parentNumber} = await apiCreateIssue(page.request, {owner, repo: repoName, title: 'shots parent'});
  const {index: childNumber} = await apiCreateIssue(page.request, {owner, repo: repoName, title: 'shots child'});
  await apiSetIssueType(page.request, owner, repoName, childNumber, typeID);
  await apiSetIssueType(page.request, owner, repoName, parentNumber, parentTypeID);
  await apiSetIssueParent(page.request, owner, repoName, childNumber, parentNumber);
  const milestoneID = await apiCreateMilestone(page.request, owner, repoName, 'shots sprint', '2030-06-01T00:00:00Z');

  await page.goto(`/${owner}/${repoName}/issues/${childNumber}`);
  await expect(page.locator('.issue-sidebar-planning')).toContainText('task');
  await page.screenshot({path: `${shotsDir}/issue-sidebar.png`, fullPage: true});

  await page.goto(`/${owner}/${repoName}/issues`);
  await expect(page.locator('.planning-type-icon svg').first()).toBeVisible();
  await page.screenshot({path: `${shotsDir}/issue-list-icons.png`, fullPage: true});

  await page.goto(`/${owner}/${repoName}/milestones/${milestoneID}/edit`);
  await expect(page.locator('[data-global-init="initPlanningMilestoneStart"]')).toBeVisible();
  await page.screenshot({path: `${shotsDir}/milestone-edit.png`, fullPage: true});
});
