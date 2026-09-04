import {applyDrop, mergeVisibleOrderIntoCell, planDrop} from './board.ts';
import type {Board, Card} from './types.ts';

function card(partial: Partial<Card> & {issue_id: number; column_id: number; sorting: number}): Card {
  return {
    number: partial.issue_id, title: `#${partial.issue_id}`, url: '', labels: [], assignees: [], assignee_avatars: [],
    is_closed: false, is_pull: false, fields: {}, points: 0, time_estimate: 0, tracked_seconds: 0,
    ...partial,
  };
}

function board(cards: Card[]): Board {
  return {
    repo_id: 1, repo_full_name: 'o/r', project_id: 1, group_by: 'none',
    columns: [{column_id: 1, title: 'Todo', default: true}, {column_id: 2, title: 'Doing', default: false}],
    groups: [{key: '', label: 'All issues', is_empty_value: true, cards: cards.length, points_total: 0, points_closed: 0, columns: [
      {column_id: 1, title: 'Todo', cards: cards.filter((c) => c.column_id === 1)},
      {column_id: 2, title: 'Doing', cards: cards.filter((c) => c.column_id === 2)},
    ]}],
    tree: [], types: [{id: 1, name: 'Bug', color: '#f00', icon: 'bug', rank: 0, sort: 0, scope: 'repo', scope_id: 1}],
    fields: [], labels: [], can_write: true, can_edit_issue: true,
  };
}

test('within-column reorder sends one order write with the whole column, in the new order', () => {
  const b = board([card({issue_id: 1, column_id: 1, sorting: 0}), card({issue_id: 2, column_id: 1, sorting: 1})]);
  const writes = planDrop(b, 'none', 2, {columnId: 1, groupKeyValue: '', cellIssueIds: [2, 1]});
  expect(writes).toEqual([{kind: 'order', columnId: 1, issueIds: [2, 1]}]);
});

test('a drop back onto its own slot issues no write', () => {
  const b = board([card({issue_id: 1, column_id: 1, sorting: 0}), card({issue_id: 2, column_id: 1, sorting: 1})]);
  const writes = planDrop(b, 'none', 2, {columnId: 1, groupKeyValue: '', cellIssueIds: [1, 2]});
  expect(writes).toEqual([]);
});

test('mergeVisibleOrderIntoCell keeps a filtered-out card in place, then planDrop keeps it in the write', () => {
  const shown1 = card({issue_id: 1, column_id: 1, sorting: 0});
  const hidden = card({issue_id: 2, column_id: 1, sorting: 1});
  const shown2 = card({issue_id: 3, column_id: 1, sorting: 2});
  const b = board([shown1, hidden, shown2]);
  // the search filter hides issue 2, so the on-screen reorder only ever sees [1, 3]
  const cellIssueIds = mergeVisibleOrderIntoCell([shown1, hidden, shown2], [3, 1]);
  expect(cellIssueIds).toEqual([3, 1, hidden.issue_id]);
  const writes = planDrop(b, 'none', 3, {columnId: 1, groupKeyValue: '', cellIssueIds});
  expect(writes).toEqual([{kind: 'order', columnId: 1, issueIds: [3, 1, 2]}]);
});

test('cross-column drop moves the card with the order write alone, fewest writes', () => {
  const b = board([
    card({issue_id: 1, column_id: 1, sorting: 0}),
    card({issue_id: 2, column_id: 2, sorting: 0}),
    card({issue_id: 3, column_id: 2, sorting: 1}),
  ]);
  const writes = planDrop(b, 'none', 1, {columnId: 2, groupKeyValue: '', cellIssueIds: [2, 1, 3]});
  expect(writes).toHaveLength(1);
  expect(writes[0]).toEqual({kind: 'order', columnId: 2, issueIds: [2, 1, 3]});
});

test('cross-group drop writes the group before the order, and keeps the other group in place', () => {
  const bug = card({issue_id: 1, column_id: 1, sorting: 0, type: 'Bug'});
  const untyped = card({issue_id: 2, column_id: 1, sorting: 1});
  const b = board([bug, untyped]);
  const writes = planDrop(b, 'type', 1, {columnId: 1, groupKeyValue: '', cellIssueIds: [1, 2]});
  expect(writes).toEqual([
    {kind: 'group', issueId: 1, groupBy: 'type', group: ''},
    {kind: 'order', columnId: 1, issueIds: [1, 2]},
  ]);
});

test('within-column reorder in a two-group board writes every group\'s cards, keeping the other group\'s order', () => {
  const alice1 = card({issue_id: 1, column_id: 1, sorting: 0, assignees: ['alice']});
  const alice2 = card({issue_id: 2, column_id: 1, sorting: 1, assignees: ['alice']});
  const bobCard = card({issue_id: 3, column_id: 1, sorting: 0, assignees: ['bob']});
  const b: Board = {
    repo_id: 1, repo_full_name: 'o/r', project_id: 1, group_by: 'assignee',
    columns: [{column_id: 1, title: 'Todo', default: true}],
    groups: [
      {
        key: 'alice', label: 'alice', is_empty_value: false, cards: 2, points_total: 0, points_closed: 0,
        columns: [{column_id: 1, title: 'Todo', cards: [alice1, alice2]}],
      },
      {
        key: 'bob', label: 'bob', is_empty_value: false, cards: 1, points_total: 0, points_closed: 0,
        columns: [{column_id: 1, title: 'Todo', cards: [bobCard]}],
      },
    ],
    tree: [], types: [], fields: [], labels: [], can_write: true, can_edit_issue: true,
  };
  // reorder alice's own two cards; bob's swimlane is untouched by the drop
  const writes = planDrop(b, 'assignee', 2, {columnId: 1, groupKeyValue: 'alice', cellIssueIds: [2, 1]});
  expect(writes).toEqual([{kind: 'order', columnId: 1, issueIds: [2, 1, 3]}]);
});

test('no group write when the drop keeps the same group', () => {
  const bug = card({issue_id: 1, column_id: 1, sorting: 0, type: 'Bug'});
  const other = card({issue_id: 2, column_id: 1, sorting: 1, type: 'Bug'});
  const b = board([bug, other]);
  const writes = planDrop(b, 'type', 1, {columnId: 1, groupKeyValue: 'Bug', cellIssueIds: [2, 1]});
  expect(writes).toEqual([{kind: 'order', columnId: 1, issueIds: [2, 1]}]);
});

test('applyDrop moves the card into the destination column and renumbers its sorting', () => {
  const b = board([
    card({issue_id: 1, column_id: 1, sorting: 0}),
    card({issue_id: 2, column_id: 2, sorting: 0}),
  ]);
  const writes = planDrop(b, 'none', 1, {columnId: 2, groupKeyValue: '', cellIssueIds: [2, 1]});
  const revert = applyDrop(b, 'none', 1, {columnId: 2, groupKeyValue: '', cellIssueIds: [2, 1]}, writes);
  expect(b.groups[0].columns[0].cards.map((c) => c.issue_id)).toEqual([]);
  expect(b.groups[0].columns[1].cards.map((c) => c.issue_id)).toEqual([2, 1]);
  expect(b.groups[0].columns[1].cards[1].column_id).toBe(2);
  expect(b.groups[0].columns[1].cards[1].sorting).toBe(1);

  revert!();
  expect(b.groups[0].columns[0].cards.map((c) => c.issue_id)).toEqual([1]);
  expect(b.groups[0].columns[1].cards.map((c) => c.issue_id)).toEqual([2]);
  expect(b.groups[0].columns[0].cards[0].column_id).toBe(1);
});
