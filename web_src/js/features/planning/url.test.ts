import {buildSearch, defaultUrlState, parseUrlState} from './url.ts';
import type {UrlState} from './url.ts';

test('parsing an empty search returns the defaults', () => {
  expect(parseUrlState('')).toEqual(defaultUrlState);
});

test('collapsed is a comma list of positive issue ids', () => {
  expect(parseUrlState('?collapsed=3,7,12').collapsed).toEqual([3, 7, 12]);
  expect(parseUrlState('?collapsed=3,x,-1,0,9').collapsed).toEqual([3, 9]);
  expect(parseUrlState('?collapsed=').collapsed).toEqual([]);
});

test('round-trips a state with every field set through the URL', () => {
  const state: UrlState = {view: 'board', q: 'is:open type:bug', group_by: 'assignee', scale: 'week', at: '2026-05-01', collapsed: [1, 2, 3]};
  expect(parseUrlState(buildSearch(state))).toEqual(state);
});

test('round-trips the defaults back to the defaults', () => {
  expect(parseUrlState(buildSearch(defaultUrlState))).toEqual(defaultUrlState);
});

test('buildSearch omits keys that are at their default', () => {
  const search = buildSearch({...defaultUrlState, q: 'bug'});
  expect(search).toBe('?q=bug');
});

test('buildSearch produces no query string when everything is default', () => {
  expect(buildSearch(defaultUrlState)).toBe('');
});
