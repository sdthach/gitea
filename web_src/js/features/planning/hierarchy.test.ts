import {allowedChildTypes, defaultChildType} from './hierarchy.ts';
import type {IssueType} from './types.ts';

function type(id: number, name: string, rank: number): IssueType {
  return {id, name, color: '', icon: '', rank, sort: 0, scope: 'repo', scope_id: 0};
}

const epic = type(1, 'epic', 1);
const story = type(2, 'story', 2);
const task = type(3, 'task', 3);
const types = [epic, story, task];

test('allowedChildTypes keeps only types ranked below the parent', () => {
  expect(allowedChildTypes(types, epic.rank)).toEqual([story, task]);
  expect(allowedChildTypes(types, story.rank)).toEqual([task]);
  expect(allowedChildTypes(types, task.rank)).toEqual([]);
});

test('defaultChildType picks the type immediately below the parent, not the most junior one', () => {
  expect(defaultChildType(types, epic.rank)).toEqual(story);
  expect(defaultChildType(types, story.rank)).toEqual(task);
});

test('defaultChildType is undefined when the parent already outranks every type', () => {
  expect(defaultChildType(types, task.rank)).toBeUndefined();
});
