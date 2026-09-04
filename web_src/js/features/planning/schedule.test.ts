import {hasKnownStart} from './schedule.ts';

test('hasKnownStart is false only for an inferred start, true for a scheduled or absent one', () => {
  expect(hasKnownStart({start_source: 'issue_created'})).toBe(false);
  expect(hasKnownStart({start_source: 'schedule'})).toBe(true);
  expect(hasKnownStart({start_source: 'none'})).toBe(true);
});
