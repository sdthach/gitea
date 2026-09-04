import {emptyGroupLabel, groupKey, orderGroupKeys} from './groups.ts';

test('type grouping keys on the trimmed type name, empty when unset', () => {
  expect(groupKey({typeName: 'Bug'}, 'type')).toBe('Bug');
  expect(groupKey({typeName: '  Feature  '}, 'type')).toBe('Feature');
  expect(groupKey({}, 'type')).toBe('');
});

test('assignee grouping keys on the lexicographically first login', () => {
  expect(groupKey({assignees: ['zoe', 'alice', 'bob']}, 'assignee')).toBe('alice');
  expect(groupKey({assignees: []}, 'assignee')).toBe('');
  expect(groupKey({}, 'assignee')).toBe('');
});

test('parent grouping keys on the root issue id, empty when the root has no children', () => {
  expect(groupKey({rootIssueId: 42, hasChildren: true}, 'parent')).toBe('42');
  expect(groupKey({rootIssueId: 42, hasChildren: false}, 'parent')).toBe('');
});

test('none grouping always returns the empty key', () => {
  expect(groupKey({typeName: 'Bug', assignees: ['alice']}, 'none')).toBe('');
});

test('order sorts the empty-value group last and every other key lexicographically', () => {
  expect(orderGroupKeys(['bravo', '', 'alpha', 'charlie'])).toEqual(['alpha', 'bravo', 'charlie', '']);
  expect(orderGroupKeys(['', ''])).toEqual(['']);
});

test('order deduplicates keys', () => {
  expect(orderGroupKeys(['a', 'a', 'b'])).toEqual(['a', 'b']);
});

test('empty-group labels name the issues they hold, per grouping', () => {
  expect(emptyGroupLabel('type')).toBe('no type assigned');
  expect(emptyGroupLabel('assignee')).toBe('unassigned');
  expect(emptyGroupLabel('parent')).toBe('no parent');
  expect(emptyGroupLabel('none')).toBe('All issues');
});
