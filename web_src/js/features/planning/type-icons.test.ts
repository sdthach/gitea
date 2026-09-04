import {getIssueTypeAssignments} from './api.ts';
import {batchByRepo, initPlanningTypeIcon, maxIssueTypeIDs, type IconTarget} from './type-icons.ts';
import type {IssueTypeAssignment} from './types.ts';

vi.mock('./api.ts', () => ({getIssueTypeAssignments: vi.fn(), sessionConfig: vi.fn(() => ({apiBase: '', token: ''}))}));

function targets(repoId: number, count: number, startAt = 1): IconTarget[] {
  return Array.from({length: count}, (_, i) => ({issueId: startAt + i, repoId}));
}

test('batchByRepo groups by repository', () => {
  const chunked = batchByRepo([...targets(1, 2), ...targets(2, 1, 100)]);
  expect(Array.from(chunked.keys())).toEqual([1, 2]);
  expect(chunked.get(1)).toEqual([[1, 2]]);
  expect(chunked.get(2)).toEqual([[100]]);
});

test('batchByRepo splits one repository into chunks of at most maxIssueTypeIDs', () => {
  const chunked = batchByRepo(targets(1, maxIssueTypeIDs + 50));
  const chunks = chunked.get(1)!;
  expect(chunks).toHaveLength(2);
  expect(chunks[0]).toHaveLength(maxIssueTypeIDs);
  expect(chunks[1]).toHaveLength(50);
});

test('batchByRepo keeps ids under the cap in a single chunk', () => {
  const chunked = batchByRepo(targets(1, maxIssueTypeIDs));
  expect(chunked.get(1)).toHaveLength(1);
});

test('initPlanningTypeIcon batches every mounted span across chunks', async () => {
  const total = maxIssueTypeIDs + 50;
  document.body.innerHTML = Array.from(
    {length: total},
    (_, i) => `<span data-issue-id="${i + 1}" data-repo-id="7" class="planning-type-icon"></span>`,
  ).join('');
  const spans = document.querySelectorAll<HTMLElement>('span[data-issue-id][data-repo-id]');
  expect(spans).toHaveLength(total);

  vi.mocked(getIssueTypeAssignments).mockImplementation(async (_config, _repoId, issueIds: number[]) =>
    issueIds.map((issueId) => ({issue_id: issueId, type_id: 1, name: 'bug', color: '#d1242f', icon: 'octicon-bug', icon_svg: '<svg></svg>'} satisfies IssueTypeAssignment)));

  for (const span of spans) await initPlanningTypeIcon(span);

  expect(getIssueTypeAssignments).toHaveBeenCalledTimes(2);
  expect(vi.mocked(getIssueTypeAssignments).mock.calls[0][2]).toHaveLength(maxIssueTypeIDs);
  expect(vi.mocked(getIssueTypeAssignments).mock.calls[1][2]).toHaveLength(50);
  for (const span of spans) expect(span.innerHTML).toBe('<svg></svg>');
});
