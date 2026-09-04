import {PX_PER_DAY, dayIndex, isWeekend, ticks, unixAtX, visibleWindow, weekendDayIndexes, xOf} from './scale.ts';

// 2024-01-01T00:00:00Z is a Monday, so week-scale ticks land on origin itself.
const ORIGIN = Date.UTC(2024, 0, 1) / 1000;
const DAY = 86400;

test('dayIndex counts whole UTC days, negative before origin', () => {
  expect(dayIndex(ORIGIN, ORIGIN)).toBe(0);
  expect(dayIndex(ORIGIN + DAY, ORIGIN)).toBe(1);
  expect(dayIndex(ORIGIN - DAY, ORIGIN)).toBe(-1);
  expect(dayIndex(ORIGIN + DAY + 3600, ORIGIN)).toBe(1);
});

test.each(Object.entries(PX_PER_DAY) as [keyof typeof PX_PER_DAY, number][])(
  'xOf places day N at N * PX_PER_DAY.%s',
  (scale, pxPerDay) => {
    expect(xOf(ORIGIN, ORIGIN, scale)).toBe(0);
    expect(xOf(ORIGIN + DAY, ORIGIN, scale)).toBe(pxPerDay);
    expect(xOf(ORIGIN + 3 * DAY, ORIGIN, scale)).toBe(3 * pxPerDay);
  },
);

test('unixAtX inverts xOf and snaps to UTC midnight', () => {
  expect(unixAtX(0, ORIGIN, 'day')).toBe(ORIGIN);
  expect(unixAtX(PX_PER_DAY.day, ORIGIN, 'day')).toBe(ORIGIN + DAY);
  expect(unixAtX(PX_PER_DAY.day * 2, ORIGIN, 'day')).toBe(ORIGIN + 2 * DAY);
  // A pixel offset that does not land on an exact day boundary still resolves to one.
  expect(unixAtX(PX_PER_DAY.day / 4, ORIGIN, 'day')).toBe(ORIGIN);
  expect(unixAtX(-PX_PER_DAY.day, ORIGIN, 'day')).toBe(ORIGIN - DAY);
});

test('isWeekend reads Saturday and Sunday, not Friday or Monday', () => {
  const saturday = ORIGIN + 5 * DAY; // 2024-01-06
  const sunday = ORIGIN + 6 * DAY; // 2024-01-07
  const friday = ORIGIN + 4 * DAY;
  expect(isWeekend(saturday)).toBe(true);
  expect(isWeekend(sunday)).toBe(true);
  expect(isWeekend(friday)).toBe(false);
  expect(isWeekend(ORIGIN)).toBe(false);
});

test('ticks at day scale marks every day inclusive of both ends', () => {
  const marks = ticks(ORIGIN, 3, 'day');
  expect(marks.map((t) => t.unix)).toEqual([ORIGIN, ORIGIN + DAY, ORIGIN + 2 * DAY, ORIGIN + 3 * DAY]);
});

test('ticks at week scale marks Mondays only', () => {
  const marks = ticks(ORIGIN, 14, 'week');
  expect(marks.map((t) => t.unix)).toEqual([ORIGIN, ORIGIN + 7 * DAY, ORIGIN + 14 * DAY]);
});

test('ticks at week scale finds the first Monday when origin is mid-week', () => {
  const wednesday = ORIGIN + 2 * DAY; // 2024-01-03
  const marks = ticks(wednesday, 10, 'week');
  expect(marks.map((t) => t.unix)).toEqual([ORIGIN + 7 * DAY]);
});

test('ticks at month scale marks month starts', () => {
  const marks = ticks(ORIGIN, 70, 'month');
  expect(marks.map((t) => t.label)).toEqual(['Jan 2024', 'Feb 2024', 'Mar 2024']);
});

test('ticks at quarter scale marks quarter starts', () => {
  const marks = ticks(ORIGIN, 200, 'quarter');
  expect(marks.map((t) => t.label)).toEqual(['Q1 2024', 'Q2 2024', 'Q3 2024']);
});

test('weekendDayIndexes lists Saturday and Sunday offsets within the window, not past its edge', () => {
  expect(weekendDayIndexes(ORIGIN, 10)).toEqual([5, 6]);
  expect(weekendDayIndexes(ORIGIN, 5)).toEqual([]);
  expect(weekendDayIndexes(ORIGIN, 6)).toEqual([5]);
});

test('visibleWindow centers on at and covers at least the requested width', () => {
  const win = visibleWindow(ORIGIN + 30 * DAY, 'day', 480); // 10 visible days at day scale
  expect(win.origin).toBeLessThan(ORIGIN + 30 * DAY);
  expect(win.endUnix).toBeGreaterThan(ORIGIN + 30 * DAY);
  expect(win.days * PX_PER_DAY.day).toBeGreaterThanOrEqual(480);
});
