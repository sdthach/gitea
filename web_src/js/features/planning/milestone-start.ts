import {createElementFromAttrs} from '../../utils/dom.ts';
import {getRoadmap, sessionConfig} from './api.ts';

function dateValue(unix: number): string {
  return unix ? new Date(unix * 1000).toISOString().slice(0, 10) : '';
}

// milestoneIdFromPath reads the id EditMilestone's own route carries, ".../milestones/{id}/edit"
// — the fragment gets no milestone id from Go data, since upstream's own EditMilestone keeps
// only the title/deadline/content strings in its template data, not the row itself.
export function milestoneIdFromPath(pathname: string): number | null {
  const match = /\/milestones\/(\d+)\/edit(?:[/?#]|$)/.exec(pathname);
  return match ? Number(match[1]) : null;
}

export async function initPlanningMilestoneStart(el: HTMLElement) {
  const milestoneId = milestoneIdFromPath(window.location.pathname);
  if (milestoneId === null) return;
  const repoId = Number(el.getAttribute('data-repo-id'));
  const postBase = el.getAttribute('data-post-base')!;

  // A roadmap that fails to load still leaves the field usable to set a start; only the
  // prefill is lost, so the write below is never blocked on this read.
  let startUnix = 0;
  try {
    const roadmap = await getRoadmap(sessionConfig(), {repoId});
    startUnix = roadmap.milestones?.find((m) => m.milestone_id === milestoneId)?.start_unix ?? 0;
  } catch {
    // startUnix keeps its initial 0; the field still renders, just with no prefill
  }

  const label = document.createElement('label');
  label.textContent = 'Start date';
  const input = createElementFromAttrs('input', {type: 'date', name: 'start', 'aria-label': 'Start date', class: 'tw-w-auto', value: dateValue(startUnix)});
  const button = createElementFromAttrs('button', {class: 'ui icon button', type: 'submit'}, 'Save start');
  const form = createElementFromAttrs('form', {
    class: 'ui fluid action input form-fetch-action', method: 'post', action: `${postBase}/milestones/${milestoneId}/schedule`,
  }, input, button);
  el.append(label, form);
}
