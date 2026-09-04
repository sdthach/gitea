import {batchByRepo, maxIssueTypeIDs, type IconTarget} from './type-icons.ts';

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
