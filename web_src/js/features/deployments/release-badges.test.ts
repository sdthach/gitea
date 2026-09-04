import {initDeploymentsReleaseBadges} from './release-badges.ts';
import * as Api from './api.ts';
import type {MatrixRow} from './types.ts';

vi.mock('./api.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof Api>();
  return {...actual, call: vi.fn()};
});

const {call, paths} = Api;

afterEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
});

// releaseEntry mirrors the anchor and heading templates/repo/release/list.tmpl renders for one
// release: the one tag anchor under .meta, and the .release-list-title heading badges append to.
function releaseEntry(tag: string): string {
  return `
    <li class="release-entry">
      <div class="meta"><a href="/owner/repo/src/tag/${tag}">${tag}</a></div>
      <div class="ui segment detail">
        <div class="flex-left-right tw-mb-2"><h4 class="release-list-title">${tag} title</h4></div>
      </div>
    </li>
  `;
}

test('each release entry gets its live-environment badges from one matrix read', async () => {
  document.body.innerHTML = `
    <ul id="release-list">${releaseEntry('v1')}${releaseEntry('v2')}</ul>
    <div id="mount" data-repo-id="7" data-api-base="/api/deployments/v1"></div>
  `;
  const rows: MatrixRow[] = [
    {
      repo_id: 7, repo_full_name: 'owner/repo', release_tag: 'v1', release_url: '', created_unix: 0,
      cells: [{environment: 'prod', sort_order: 1, state: 'live', symbol: '✔ now', successes: 1, run_id: 1, run_url: '', occurred_unix: 0}],
    },
    {
      repo_id: 7, repo_full_name: 'owner/repo', release_tag: 'v2', release_url: '', created_unix: 0,
      cells: [{environment: 'qa', sort_order: 0, state: 'live', symbol: '✔ now', successes: 1, run_id: 2, run_url: '', occurred_unix: 0}],
    },
  ];
  vi.mocked(call).mockResolvedValue(rows);

  await initDeploymentsReleaseBadges(document.querySelector('#mount')!);

  expect(call).toHaveBeenCalledTimes(1);
  expect(vi.mocked(call).mock.calls[0][1]).toBe(`${paths.matrix}?repo_id=7&limit=200`);

  const titles = document.querySelectorAll('.release-list-title');
  expect(titles[0].textContent).toContain('prod');
  expect(titles[1].textContent).toContain('qa');
});
