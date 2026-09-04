import {buildTree, depthOf, rootOf, treeOrder, visibleRows} from './tree.ts';

test('rootOf climbs to the top of a chain', () => {
  const tree = buildTree([{issue_id: 2, parent_issue_id: 1}, {issue_id: 3, parent_issue_id: 2}]);
  expect(rootOf(tree, 3)).toBe(1);
  expect(rootOf(tree, 1)).toBe(1);
});

test('depthOf counts the hops to the root', () => {
  const tree = buildTree([{issue_id: 2, parent_issue_id: 1}, {issue_id: 3, parent_issue_id: 2}]);
  expect(depthOf(tree, 1)).toBe(0);
  expect(depthOf(tree, 2)).toBe(1);
  expect(depthOf(tree, 3)).toBe(2);
});

test('a cycle does not hang rootOf or depthOf', () => {
  const tree = buildTree([
    {issue_id: 1, parent_issue_id: 3},
    {issue_id: 2, parent_issue_id: 1},
    {issue_id: 3, parent_issue_id: 2},
  ]);
  expect([1, 2, 3]).toContain(rootOf(tree, 1));
  expect(depthOf(tree, 1)).toBeLessThan(100);
}, 2000);

test('visibleRows hides every row under a collapsed ancestor', () => {
  const rows = [
    {issueId: 1, parentIssueId: undefined},
    {issueId: 2, parentIssueId: 1},
    {issueId: 3, parentIssueId: 2},
  ];
  expect(visibleRows(rows, new Set([2])).map((r) => r.issueId)).toEqual([1, 2]);
  expect(visibleRows(rows, new Set()).map((r) => r.issueId)).toEqual([1, 2, 3]);
});

test('a cycle does not hang visibleRows', () => {
  const rows = [
    {issueId: 1, parentIssueId: 3},
    {issueId: 2, parentIssueId: 1},
    {issueId: 3, parentIssueId: 2},
  ];
  expect(visibleRows(rows, new Set()).map((r) => r.issueId).sort()).toEqual([1, 2, 3]);
}, 2000);

test('treeOrder places each parent immediately before its own children', () => {
  const rows = [
    {issueId: 3, parentIssueId: 1},
    {issueId: 1, parentIssueId: undefined},
    {issueId: 4, parentIssueId: 3},
    {issueId: 2, parentIssueId: undefined},
  ];
  expect(treeOrder(rows).map((r) => r.issueId)).toEqual([1, 3, 4, 2]);
});

test('a cycle does not hang treeOrder, and every row still comes out once', () => {
  const rows = [
    {issueId: 1, parentIssueId: 3},
    {issueId: 2, parentIssueId: 1},
    {issueId: 3, parentIssueId: 2},
  ];
  expect(treeOrder(rows).map((r) => r.issueId).sort()).toEqual([1, 2, 3]);
}, 2000);
