import {filterRows, matchesQuery, tokenize} from './filter.ts';
import type {FilterRow} from './filter.ts';

function row(partial: Partial<FilterRow>): FilterRow {
  return {title: '', isClosed: false, ...partial};
}

test('free words match the title case-insensitively', () => {
  expect(matchesQuery(row({title: 'Fix the Login bug'}), 'login')).toBe(true);
  expect(matchesQuery(row({title: 'Fix the Login bug'}), 'signup')).toBe(false);
});

test('is:open and is:closed', () => {
  expect(matchesQuery(row({isClosed: false}), 'is:open')).toBe(true);
  expect(matchesQuery(row({isClosed: false}), 'is:closed')).toBe(false);
  expect(matchesQuery(row({isClosed: true}), 'is:closed')).toBe(true);
});

test('type, assignee, label, milestone are exact and case-insensitive', () => {
  const r = row({type: 'Bug', assignees: ['Alice'], labels: ['Urgent'], milestone: 'Sprint 1'});
  expect(matchesQuery(r, 'type:bug')).toBe(true);
  expect(matchesQuery(r, 'assignee:alice')).toBe(true);
  expect(matchesQuery(r, 'label:urgent')).toBe(true);
  expect(matchesQuery(r, 'milestone:"Sprint 1"')).toBe(true);
  expect(matchesQuery(r, 'type:feature')).toBe(false);
});

test('parent:#N and parent:none', () => {
  expect(matchesQuery(row({parentIssueId: 12}), 'parent:#12')).toBe(true);
  expect(matchesQuery(row({parentIssueId: 12}), 'parent:12')).toBe(true);
  expect(matchesQuery(row({parentIssueId: 12}), 'parent:#13')).toBe(false);
  expect(matchesQuery(row({}), 'parent:none')).toBe(true);
  expect(matchesQuery(row({parentIssueId: 12}), 'parent:none')).toBe(false);
});

test('no: shorthand for an unset built-in field', () => {
  expect(matchesQuery(row({}), 'no:type')).toBe(true);
  expect(matchesQuery(row({type: 'bug'}), 'no:type')).toBe(false);
  expect(matchesQuery(row({}), 'no:assignee')).toBe(true);
  expect(matchesQuery(row({assignees: ['a']}), 'no:assignee')).toBe(false);
  expect(matchesQuery(row({}), 'no:milestone')).toBe(true);
  expect(matchesQuery(row({}), 'no:parent')).toBe(true);
});

test('unknown keys are treated as free text', () => {
  expect(matchesQuery(row({title: 'status:review needed'}), 'status:review')).toBe(true);
  expect(matchesQuery(row({title: 'nothing here'}), 'status:review')).toBe(false);
});

test('custom int field: eq, comparisons and a range', () => {
  const kinds = {points: 'int' as const};
  expect(matchesQuery(row({fields: {points: 5}}), 'points:5', kinds)).toBe(true);
  expect(matchesQuery(row({fields: {points: 5}}), 'points:>3', kinds)).toBe(true);
  expect(matchesQuery(row({fields: {points: 5}}), 'points:>=5', kinds)).toBe(true);
  expect(matchesQuery(row({fields: {points: 5}}), 'points:<3', kinds)).toBe(false);
  expect(matchesQuery(row({fields: {points: 5}}), 'points:<=5', kinds)).toBe(true);
  expect(matchesQuery(row({fields: {points: 5}}), 'points:1..10', kinds)).toBe(true);
  expect(matchesQuery(row({fields: {points: 15}}), 'points:1..10', kinds)).toBe(false);
});

test('custom date field: a range in YYYY-MM-DD', () => {
  const kinds = {due: 'date' as const};
  expect(matchesQuery(row({fields: {due: '2026-05-15'}}), 'due:2026-05-01..2026-05-31', kinds)).toBe(true);
  expect(matchesQuery(row({fields: {due: '2026-06-01'}}), 'due:2026-05-01..2026-05-31', kinds)).toBe(false);
  expect(matchesQuery(row({fields: {due: '2026-05-15'}}), 'due:>2026-05-01', kinds)).toBe(true);
});

test('custom text/select field: exact match, quoted value with a space', () => {
  const kinds = {status: 'select' as const};
  expect(matchesQuery(row({fields: {status: 'in review'}}), 'status:"in review"', kinds)).toBe(true);
  expect(matchesQuery(row({fields: {status: 'in review'}}), 'status:"in progress"', kinds)).toBe(false);
});

test('a quoted value keeps its spaces as one token', () => {
  expect(tokenize('milestone:"Sprint 1" is:open')).toEqual(['milestone:Sprint 1', 'is:open']);
});

test('an unset custom field never matches', () => {
  expect(matchesQuery(row({fields: {}}), 'points:>0', {points: 'int'})).toBe(false);
});

test('every clause in a query must match', () => {
  const r = row({title: 'login bug', isClosed: false, type: 'bug'});
  expect(matchesQuery(r, 'login is:open type:bug')).toBe(true);
  expect(matchesQuery(r, 'login is:closed type:bug')).toBe(false);
});

test('filterRows keeps only matching rows, and an empty query keeps everything', () => {
  const rows = [row({title: 'a', isClosed: false}), row({title: 'b', isClosed: true})];
  expect(filterRows(rows, 'is:closed').map((r) => r.title)).toEqual(['b']);
  expect(filterRows(rows, ' '.repeat(3))).toHaveLength(2);
});
