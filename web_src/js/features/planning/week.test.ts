import {shiftWeek, weekBounds} from './week.ts';

function unix(iso: string): number {
  return Math.floor(Date.parse(`${iso}T00:00:00Z`) / 1000);
}

test('weekBounds resolves Monday through Sunday for a mid-week date', () => {
  const {startUnix, endUnix} = weekBounds(unix('2026-03-11')); // a Wednesday
  expect(startUnix).toBe(unix('2026-03-09'));
  expect(endUnix).toBe(unix('2026-03-15'));
});

test('weekBounds treats Sunday as the end of its own week, not the start of the next', () => {
  const {startUnix, endUnix} = weekBounds(unix('2026-03-15'));
  expect(startUnix).toBe(unix('2026-03-09'));
  expect(endUnix).toBe(unix('2026-03-15'));
});

test('shiftWeek moves by whole weeks in either direction', () => {
  expect(shiftWeek(unix('2026-03-11'), 1)).toBe(unix('2026-03-18'));
  expect(shiftWeek(unix('2026-03-11'), -1)).toBe(unix('2026-03-04'));
});
