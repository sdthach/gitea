import {begin, rowAt, targetRowIndex} from './drag.ts';
import type {DragBar, DragContext, RowGeometry} from './drag.ts';

const ORIGIN = Date.UTC(2024, 0, 1) / 1000;
const DAY = 86400;

const assigneeRows: RowGeometry = {
  rowHeight: 40,
  rows: [
    {key: 'alice', kind: 'assignee', top: 0},
    {key: 'bob', kind: 'assignee', top: 40},
    {key: '', kind: 'assignee', top: 80},
  ],
};

function ctx(rowGeometry: RowGeometry = assigneeRows): DragContext {
  return {origin: ORIGIN, scale: 'day', rowGeometry};
}

function bar(partial: Partial<DragBar> = {}): DragBar {
  return {issueId: 1, startUnix: ORIGIN + 5 * DAY, endUnix: ORIGIN + 8 * DAY, rowKey: 'alice', ...partial};
}

test.each([
  [48, 1], [96, 2], [-48, -1], [0, 0], [10, 0], [30, 1],
])('move by %ipx shifts both dates by %i day(s), keeping duration', (dx, days) => {
  const b = bar();
  const drag = begin('move', b, 0, 0, ctx());
  const proposal = drag.update(dx, 0);
  expect(proposal.start).toBe(b.startUnix + days * DAY);
  expect(proposal.end).toBe(b.endUnix + days * DAY);
});

test('resize-start never crosses past one day before end, however far the pointer drags', () => {
  const b = bar();
  const drag = begin('resize-start', b, 0, 0, ctx());
  const proposal = drag.update(48 * 100, 0); // far past the end
  expect(proposal.start).toBe(b.endUnix - DAY);
  expect(proposal.end).toBe(b.endUnix);
});

test('resize-end never crosses past one day after start, however far the pointer drags', () => {
  const b = bar();
  const drag = begin('resize-end', b, 0, 0, ctx());
  const proposal = drag.update(-48 * 100, 0); // far before the start
  expect(proposal.end).toBe(b.startUnix + DAY);
  expect(proposal.start).toBe(b.startUnix);
});

test('resize-start moves the start and keeps the end fixed', () => {
  const b = bar();
  const drag = begin('resize-start', b, 0, 0, ctx());
  const proposal = drag.update(96, 0);
  expect(proposal.start).toBe(b.startUnix + 2 * DAY);
  expect(proposal.end).toBe(b.endUnix);
});

test('resize-end moves the end and keeps the start fixed', () => {
  const b = bar();
  const drag = begin('resize-end', b, 0, 0, ctx());
  const proposal = drag.update(96, 0);
  expect(proposal.end).toBe(b.endUnix + 2 * DAY);
  expect(proposal.start).toBe(b.startUnix);
});

test.each([
  [10, 'alice'], [45, 'bob'], [90, ''], [-5, 'alice'], [1000, ''],
])('row drag at y=%i resolves to row %j, clamped to the nearest lane', (y, key) => {
  const drag = begin('row', bar(), 0, 0, ctx());
  const proposal = drag.update(0, y);
  expect(proposal.row).toBe(key);
  expect(proposal.start).toBe(bar().startUnix);
  expect(proposal.end).toBe(bar().endUnix);
});

test('rowAt returns undefined for an empty row set', () => {
  expect(rowAt({rowHeight: 40, rows: []}, 10)).toBeUndefined();
});

test('targetRowIndex resolves by index, not by which row shares a lane key', () => {
  expect(targetRowIndex(1, 'up', 3)).toBe(0);
  expect(targetRowIndex(1, 'down', 3)).toBe(2);
  expect(targetRowIndex(0, 'up', 3)).toBeNull();
  expect(targetRowIndex(2, 'down', 3)).toBeNull();
  // Two bars in the same lane sit at different indexes despite sharing a row key: each still
  // resolves to its own neighbor rather than both jumping from index 0.
  expect(targetRowIndex(2, 'up', 4)).toBe(1);
});

test('commit with no movement writes nothing', () => {
  const drag = begin('move', bar(), 0, 0, ctx());
  drag.update(0, 0);
  expect(drag.commit()).toEqual([]);
});

test('commit after cancel writes nothing, even after an update that moved it', () => {
  const drag = begin('move', bar(), 0, 0, ctx());
  drag.update(96, 0);
  drag.cancel();
  expect(drag.commit()).toEqual([]);
});

test('commit with dates changed sends one dates write, UTC YYYY-MM-DD', () => {
  const b = bar();
  const drag = begin('move', b, 0, 0, ctx());
  drag.update(48, 0);
  expect(drag.commit()).toEqual([
    {kind: 'dates', issueId: 1, start: '2024-01-07', end: '2024-01-10'},
  ]);
});

test('commit with an assignee row change sends a group write', () => {
  const drag = begin('row', bar(), 0, 0, ctx());
  drag.update(0, 45); // bob's band
  expect(drag.commit()).toEqual([
    {kind: 'group', issueId: 1, groupBy: 'assignee', group: 'bob'},
  ]);
});

test('commit with a parent row change sends a parent write', () => {
  const parentRows: RowGeometry = {rowHeight: 40, rows: [{key: '10', kind: 'parent', top: 0}, {key: '20', kind: 'parent', top: 40}]};
  const drag = begin('row', bar({rowKey: '10'}), 0, 0, ctx(parentRows));
  drag.update(0, 45);
  expect(drag.commit()).toEqual([
    {kind: 'parent', issueId: 1, parentIssueId: 20},
  ]);
});

test('commit with a milestone row change sends a milestone write', () => {
  const milestoneRows: RowGeometry = {rowHeight: 40, rows: [{key: '1', kind: 'milestone', top: 0}, {key: '2', kind: 'milestone', top: 40}]};
  const drag = begin('row', bar({rowKey: '1'}), 0, 0, ctx(milestoneRows));
  drag.update(0, 45);
  expect(drag.commit()).toEqual([
    {kind: 'milestone', issueId: 1, milestoneId: 2},
  ]);
});

test('commit with both dates and row changed by one diagonal move sends both writes', () => {
  const drag = begin('move', bar(), 0, 0, ctx());
  drag.update(48, 45); // one day right, into bob's band
  expect(drag.commit()).toEqual(expect.arrayContaining([
    {kind: 'dates', issueId: 1, start: '2024-01-07', end: '2024-01-10'},
    {kind: 'group', issueId: 1, groupBy: 'assignee', group: 'bob'},
  ]));
  expect(drag.commit()).toHaveLength(2);
});

test('move with the pointer staying in the bar\'s own row band leaves the row unchanged', () => {
  const drag = begin('move', bar(), 0, 10, ctx()); // starts inside alice's own band
  drag.update(48, 10);
  expect(drag.commit()).toEqual([
    {kind: 'dates', issueId: 1, start: '2024-01-07', end: '2024-01-10'},
  ]);
});
