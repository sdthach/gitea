// The project page's own view state, round-tripped through the URL query so a link
// reproduces what its sender saw.

export type UrlState = {
  view: string;
  q: string;
  group_by: string;
  scale: string;
  at: string;
  collapsed: number[];
};

export const defaultUrlState: UrlState = {view: 'table', q: '', group_by: 'none', scale: '', at: '', collapsed: []};

export function parseUrlState(search: string): UrlState {
  const params = new URLSearchParams(search);
  const collapsedRaw = params.get('collapsed') ?? '';
  const collapsed = collapsedRaw ?
    collapsedRaw.split(',').map((s) => Number(s.trim())).filter((n) => Number.isInteger(n) && n > 0) :
    [];
  return {
    view: params.get('view') || defaultUrlState.view,
    q: params.get('q') ?? defaultUrlState.q,
    group_by: params.get('group_by') || defaultUrlState.group_by,
    scale: params.get('scale') ?? defaultUrlState.scale,
    at: params.get('at') ?? defaultUrlState.at,
    collapsed,
  };
}

export function buildSearch(state: UrlState): string {
  const params = new URLSearchParams();
  if (state.view && state.view !== defaultUrlState.view) params.set('view', state.view);
  if (state.q) params.set('q', state.q);
  if (state.group_by && state.group_by !== defaultUrlState.group_by) params.set('group_by', state.group_by);
  if (state.scale) params.set('scale', state.scale);
  if (state.at) params.set('at', state.at);
  if (state.collapsed.length) params.set('collapsed', state.collapsed.join(','));
  const qs = params.toString();
  return qs ? `?${qs}` : '';
}

// applyUrlState pushes state into the address bar without a navigation, so filtering or
// switching tabs never adds a history entry.
export function applyUrlState(state: UrlState): void {
  const search = buildSearch(state);
  const url = `${window.location.pathname}${search}${window.location.hash}`;
  window.history.replaceState(window.history.state, '', url);
}
