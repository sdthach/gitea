import {call, paths} from './api.ts';
import type {DeploymentsApiConfig} from './api.ts';
import type {MatrixRow} from './types.ts';

// initDeploymentsReleaseBadges draws each release entry's environment badges from the matrix
// endpoint — the same projection /deployments renders — so the release page introduces no
// second answer to "what is live where". It reads el's own data attributes rather than
// ctx.PageData because it mounts on the release list fragment, not a page of its own.
export async function initDeploymentsReleaseBadges(el: HTMLElement) {
  const base = el.getAttribute('data-api-base');
  const repoId = Number(el.getAttribute('data-repo-id'));
  if (!base || !repoId) return;

  // Each entry's tag link is the one anchor whose href names a tag under this repository.
  const entries = new Map<string, Element>();
  for (const entry of document.querySelectorAll('#release-list .release-entry')) {
    const link = entry.querySelector<HTMLAnchorElement>('.meta a[href*="/src/tag/"]');
    const tag = link?.textContent.trim();
    const title = entry.querySelector('.release-list-title');
    if (tag && title) entries.set(tag, title);
  }
  if (!entries.size) return;

  const config: DeploymentsApiConfig = {apiBase: base, appSubUrl: '', token: ''};
  let rows: MatrixRow[];
  try {
    rows = await call<MatrixRow[]>(config, `${paths.matrix}?repo_id=${repoId}&limit=200`);
  } catch {
    // The release page is Gitea's and stands on its own; a deployments API that is not
    // answering leaves it exactly as it was.
    return;
  }

  for (const row of rows) {
    const title = entries.get(row.release_tag);
    if (!title) continue;
    // Only the cells that actually hold the release: an environment the release has never
    // reached is not news on the release page.
    const live = row.cells.filter((cell) => cell.state === 'live');
    if (!live.length) continue;
    const holder = document.createElement('span');
    holder.className = 'tw-flex tw-gap-1';
    for (const cell of live) {
      const badge = document.createElement('a');
      badge.className = 'ui label tw-text-12';
      badge.href = `${base.replace('/api/deployments/v1', '')}/deployments`;
      badge.textContent = `${cell.symbol} ${cell.environment}`;
      badge.title = `${row.release_tag} is live in ${cell.environment}`;
      holder.append(badge);
    }
    title.append(holder);
  }
}
