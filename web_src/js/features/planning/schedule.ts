// A bar with an inferred start exists only to justify itself in the "Needs a start" panel; it
// carries no real commitment to draw on the timeline.

import type {Bar} from './types.ts';

export function hasKnownStart(bar: Pick<Bar, 'start_source'>): boolean {
  return bar.start_source !== 'issue_created';
}
