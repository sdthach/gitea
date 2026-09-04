import {milestoneIdFromPath} from './milestone-start.ts';

test('milestoneIdFromPath reads the id EditMilestone\'s own route carries', () => {
  expect(milestoneIdFromPath('/user2/repo1/milestones/5/edit')).toBe(5);
  expect(milestoneIdFromPath('/org/repo/milestones/42/edit?foo=bar')).toBe(42);
});

test('milestoneIdFromPath is null off the edit route, including the new-milestone form', () => {
  expect(milestoneIdFromPath('/user2/repo1/milestones/new')).toBeNull();
  expect(milestoneIdFromPath('/user2/repo1/milestones')).toBeNull();
});
