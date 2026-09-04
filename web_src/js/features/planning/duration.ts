// The Time tab's own duration input: "1h 30m", "2h", "45m" — parsed to seconds, the unit
// Gitea's own POST .../times takes, and formatted back the same way for display. Pure, no DOM.

const DURATION_TOKEN = /^(\d+(?:\.\d+)?)\s*(h|m)$/i;

// parseDurationSeconds reads a whitespace-separated run of "<number>h" / "<number>m" tokens —
// each unit at most once, in either order — into whole seconds. Anything it cannot make sense
// of, including an empty string or a bare number with no unit, is null: a duration typo must
// refuse rather than silently log zero or truncate to whichever token it did understand.
export function parseDurationSeconds(input: string): number | null {
  const trimmed = input.trim();
  if (!trimmed) return null;
  const seen = new Set<string>();
  let seconds = 0;
  for (const token of trimmed.split(/\s+/)) {
    const m = DURATION_TOKEN.exec(token);
    if (!m) return null;
    const unit = m[2].toLowerCase();
    if (seen.has(unit)) return null;
    seen.add(unit);
    seconds += Number(m[1]) * (unit === 'h' ? 3600 : 60);
  }
  return seconds > 0 ? Math.round(seconds) : null;
}

// formatDurationSeconds is parseDurationSeconds's own inverse for display: whole hours and
// minutes, dropping a zero part rather than printing "1h 0m". Anything under a minute reads as
// "0m" instead of vanishing, since a logged entry is never actually zero seconds.
export function formatDurationSeconds(seconds: number): string {
  const totalMinutes = Math.round(seconds / 60);
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  if (hours && minutes) return `${hours}h ${minutes}m`;
  if (hours) return `${hours}h`;
  return `${minutes}m`;
}

// elapsedLabel is a running stopwatch's own display: hh:mm:ss (or mm:ss under an hour) counted
// from startedUnix to nowUnix, clamped to zero so a clock skew never prints a negative time.
export function elapsedLabel(startedUnix: number, nowUnix: number): string {
  const total = Math.max(0, Math.floor(nowUnix - startedUnix));
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = total % 60;
  const pad = (n: number) => String(n).padStart(2, '0');
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(seconds)}` : `${minutes}:${pad(seconds)}`;
}
