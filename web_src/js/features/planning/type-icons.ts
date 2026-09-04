import {getIssueTypeAssignments, sessionConfig} from './api.ts';

// maxIssueTypeIDs matches GET /issue-type-assignments' own cap; a page with more issues than
// this on screen already paginates, so no single call here ever needs more.
export const maxIssueTypeIDs = 200;

export type IconTarget = {issueId: number; repoId: number};

// batchByRepo groups every icon mount on the page by repository, then splits each repository's
// ids into requests of at most maxIssueTypeIDs — a page listing issues across many repositories
// (a dashboard) still resolves each repository's icons in one call apiece.
export function batchByRepo(targets: IconTarget[]): Map<number, number[][]> {
  const byRepo = new Map<number, number[]>();
  for (const {issueId, repoId} of targets) {
    const ids = byRepo.get(repoId);
    if (ids) ids.push(issueId); else byRepo.set(repoId, [issueId]);
  }
  const chunked = new Map<number, number[][]>();
  for (const [repoId, ids] of byRepo) {
    const chunks: number[][] = [];
    for (let i = 0; i < ids.length; i += maxIssueTypeIDs) chunks.push(ids.slice(i, i + maxIssueTypeIDs));
    chunked.set(repoId, chunks);
  }
  return chunked;
}

// started guards the whole page: every span's own data-global-init call would otherwise repeat
// the same batch, since callGlobalInitFunc only dedupes a single element, not the group.
let started = false;

export async function initPlanningTypeIcon(_el: HTMLElement) {
  if (started) return;
  started = true;

  const spans = Array.from(document.querySelectorAll<HTMLElement>('.planning-type-icon'));
  const byIssueId = new Map<number, HTMLElement[]>();
  const targets: IconTarget[] = [];
  for (const span of spans) {
    const issueId = Number(span.getAttribute('data-issue-id'));
    const repoId = Number(span.getAttribute('data-repo-id'));
    targets.push({issueId, repoId});
    const list = byIssueId.get(issueId);
    if (list) list.push(span); else byIssueId.set(issueId, [span]);
  }

  const config = sessionConfig();
  for (const [repoId, chunks] of batchByRepo(targets)) {
    for (const ids of chunks) {
      let rows;
      try {
        rows = await getIssueTypeAssignments(config, repoId, ids);
      } catch {
        continue; // no type icon is not an error a reader needs to see
      }
      for (const row of rows) {
        for (const span of byIssueId.get(row.issue_id) ?? []) {
          span.innerHTML = row.icon_svg;
          span.style.color = row.color;
          span.title = row.name;
        }
      }
    }
  }
}
