import {elapsedLabel, formatDurationSeconds, parseDurationSeconds} from './duration.ts';

test('parseDurationSeconds reads hours and minutes as seconds, not minutes', () => {
  expect(parseDurationSeconds('1h 30m')).toBe(5400);
  expect(parseDurationSeconds('2h')).toBe(7200);
  expect(parseDurationSeconds('45m')).toBe(2700);
  expect(parseDurationSeconds('30m 1h')).toBe(5400);
});

test('parseDurationSeconds refuses garbage rather than guessing', () => {
  expect(parseDurationSeconds('')).toBeNull();
  expect(parseDurationSeconds('90')).toBeNull();
  expect(parseDurationSeconds('1h 1h')).toBeNull();
  expect(parseDurationSeconds('abc')).toBeNull();
});

test('formatDurationSeconds is parseDurationSeconds\'s own inverse', () => {
  expect(formatDurationSeconds(5400)).toBe('1h 30m');
  expect(formatDurationSeconds(7200)).toBe('2h');
  expect(formatDurationSeconds(2700)).toBe('45m');
});

test('elapsedLabel counts up from the stopwatch\'s own start, clamped at zero', () => {
  expect(elapsedLabel(1000, 1090)).toBe('1:30');
  expect(elapsedLabel(1000, 1000 + 3661)).toBe('1:01:01');
  expect(elapsedLabel(1000, 990)).toBe('0:00');
});
