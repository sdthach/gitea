// The roadmap's time geometry: pixel-per-day math over UTC days, kept pure and DOM-free.

import dayjs from 'dayjs';
import utc from 'dayjs/plugin/utc.js';

dayjs.extend(utc);

export type Scale = 'day' | 'week' | 'month' | 'quarter';

export const PX_PER_DAY: Record<Scale, number> = {day: 48, week: 16, month: 5, quarter: 2};

// dayIndex is how many UTC calendar days unix sits after origin, negative when it sits before.
export function dayIndex(unix: number, origin: number): number {
  return dayjs.utc(unix * 1000).startOf('day').diff(dayjs.utc(origin * 1000).startOf('day'), 'day');
}

export function xOf(unix: number, origin: number, scale: Scale): number {
  return dayIndex(unix, origin) * PX_PER_DAY[scale];
}

// unixAtX inverts xOf, snapped to UTC midnight: a pixel offset that lands mid-day (any offset
// not an exact multiple of the scale's width) rounds to its nearest day rather than drifting.
export function unixAtX(x: number, origin: number, scale: Scale): number {
  const days = Math.round(x / PX_PER_DAY[scale]);
  return dayjs.utc(origin * 1000).startOf('day').add(days, 'day').unix();
}

export function isWeekend(unix: number): boolean {
  const day = dayjs.utc(unix * 1000).day();
  return day === 0 || day === 6;
}

// weekendDayIndexes lists which day offsets from origin, up to days exclusive, fall on a
// Saturday or Sunday — the day columns RoadmapAxis.vue and each row shade.
export function weekendDayIndexes(origin: number, days: number): number[] {
  const out: number[] = [];
  for (let i = 0; i < days; i++) {
    if (isWeekend(origin + i * 86400)) out.push(i);
  }
  return out;
}

export type Tick = {unix: number; x: number; label: string};

function tickLabel(d: dayjs.Dayjs, scale: Scale): string {
  switch (scale) {
    case 'month': return d.format('MMM YYYY');
    case 'quarter': return `Q${Math.floor(d.month() / 3) + 1} ${d.format('YYYY')}`;
    default: return d.format('MMM D');
  }
}

function firstMonday(d: dayjs.Dayjs): dayjs.Dayjs {
  const delta = (1 - d.day() + 7) % 7;
  return d.add(delta, 'day');
}

function quarterStart(d: dayjs.Dayjs): dayjs.Dayjs {
  const quarterMonth = Math.floor(d.month() / 3) * 3;
  return d.startOf('month').month(quarterMonth);
}

// ticks lists the axis marks over [origin, origin + days] (inclusive of both ends): every day
// at day scale, Mondays at week scale, month starts at month scale, quarter starts at quarter
// scale — the unit RoadmapAxis.vue reads its column widths from.
export function ticks(origin: number, days: number, scale: Scale): Tick[] {
  const start = dayjs.utc(origin * 1000).startOf('day');
  const end = start.add(days, 'day');
  let cursor: dayjs.Dayjs;
  let step: [number, 'day' | 'month'];
  switch (scale) {
    case 'week':
      cursor = firstMonday(start);
      step = [7, 'day'];
      break;
    case 'month':
      cursor = start.startOf('month');
      if (cursor.isBefore(start)) cursor = cursor.add(1, 'month');
      step = [1, 'month'];
      break;
    case 'quarter':
      cursor = quarterStart(start);
      if (cursor.isBefore(start)) cursor = cursor.add(3, 'month');
      step = [3, 'month'];
      break;
    default:
      cursor = start;
      step = [1, 'day'];
  }
  const out: Tick[] = [];
  while (!cursor.isAfter(end)) {
    const unix = cursor.unix();
    out.push({unix, x: xOf(unix, origin, scale), label: tickLabel(cursor, scale)});
    cursor = cursor.add(step[0], step[1]);
  }
  return out;
}

export type Window = {origin: number; days: number; endUnix: number};

// visibleWindow centers on at (a unix timestamp), spanning enough days to fill widthPx at scale
// plus a fixed pad on each side so a drag near an edge has somewhere to scroll into.
export function visibleWindow(at: number, scale: Scale, widthPx: number): Window {
  const visibleDays = Math.max(1, Math.ceil(widthPx / PX_PER_DAY[scale]));
  const pad = Math.ceil(visibleDays / 2);
  const totalDays = visibleDays + pad * 2;
  const center = dayjs.utc(at * 1000).startOf('day');
  const origin = center.subtract(pad, 'day');
  return {origin: origin.unix(), days: totalDays, endUnix: origin.add(totalDays, 'day').unix()};
}
