// The Time tab's own week window: pure, no DOM — Monday through Sunday, in UTC days, matching
// GetTimesheet's own default window (services/planning's CurrentISOWeek).

const DAY_SECONDS = 86400;

export type Week = {startUnix: number; endUnix: number};

// weekBounds resolves the Monday-Sunday ISO week containing atUnix, a UTC-midnight unix
// timestamp. endUnix is the Sunday itself (not the following Monday), so both ends are days
// the window actually covers, the same inclusive convention the API's from/to dates use.
export function weekBounds(atUnix: number): Week {
  const day = new Date(atUnix * 1000).getUTCDay(); // 0 = Sunday .. 6 = Saturday
  const mondayOffset = day === 0 ? -6 : 1 - day;
  const startUnix = atUnix + mondayOffset * DAY_SECONDS;
  return {startUnix, endUnix: startUnix + 6 * DAY_SECONDS};
}

// shiftWeek moves atUnix by whole weeks, for the tab's own back/forward navigation.
export function shiftWeek(atUnix: number, weeks: number): number {
  return atUnix + weeks * 7 * DAY_SECONDS;
}
