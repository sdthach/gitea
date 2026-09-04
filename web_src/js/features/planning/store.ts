import {reactive} from 'vue';
import {ApiError, getBoard, getProjectViews, getRoadmap, createProjectView, deleteProjectView} from './api.ts';
import type {Board, PlanningProjectConfig, ProjectView, Roadmap} from './types.ts';

export type PlanningError = {
  message: string;
  suggestedAction: string;
};

function toError(err: unknown): PlanningError {
  if (err instanceof ApiError) return {message: err.message, suggestedAction: err.suggestedAction};
  return {message: err instanceof Error ? err.message : String(err), suggestedAction: ''};
}

const REFRESH_INTERVAL_MS = 30000;

export function createPlanningStore(config: PlanningProjectConfig) {
  // overrides holds an inline edit's optimistic value until the next full load or refresh
  // replaces board/roadmap outright, at which point the fetched data is authoritative and a
  // stale override would otherwise keep masking it forever.
  const overrides: Record<number, Record<string, unknown>> = {};
  const state = reactive({
    config,
    board: null as Board | null,
    roadmap: null as Roadmap | null,
    views: [] as ProjectView[],
    boardError: null as PlanningError | null,
    roadmapError: null as PlanningError | null,
    viewsError: null as PlanningError | null,
    writeError: null as PlanningError | null,
    loadingBoard: false,
    loadingRoadmap: false,
    overrides,
  });

  // setOverride merges partial into issueId's own override, applied on top of the fetched row
  // until loadAll or refresh next replaces board/roadmap outright. A component reads and
  // writes overrides through this rather than the state field directly, so an edit is a store
  // method call, not a mutation of a prop the store itself is passed as.
  function setOverride(issueId: number, partial: Record<string, unknown>): void {
    state.overrides[issueId] = {...state.overrides[issueId], ...partial};
  }

  async function loadBoard(): Promise<void> {
    state.loadingBoard = true;
    try {
      state.board = await getBoard(state.config, {repoId: state.config.repoId, projectId: state.config.projectId});
      state.boardError = null;
    } catch (err) {
      state.boardError = toError(err);
    } finally {
      state.loadingBoard = false;
    }
  }

  async function loadRoadmap(): Promise<void> {
    state.loadingRoadmap = true;
    try {
      state.roadmap = await getRoadmap(state.config, {repoId: state.config.repoId});
      state.roadmapError = null;
    } catch (err) {
      state.roadmapError = toError(err);
    } finally {
      state.loadingRoadmap = false;
    }
  }

  // loadAll fetches board and roadmap in parallel: a board failure sets boardError only, so
  // a repository with Projects disabled still renders the roadmap-backed views.
  async function loadAll(): Promise<void> {
    await Promise.all([loadBoard(), loadRoadmap()]);
    state.overrides = {};
  }

  async function loadViews(): Promise<void> {
    if (!state.config.repoFullName) return;
    try {
      const result = await getProjectViews(state.config, state.config.projectId, state.config.repoFullName);
      state.views = result.views;
      state.viewsError = null;
    } catch (err) {
      state.viewsError = toError(err);
    }
  }

  async function saveView(name: string, query: string): Promise<void> {
    try {
      const result = await createProjectView(state.config, state.config.projectId, {name, query, repo: state.config.repoFullName});
      state.views = result.views;
      state.writeError = null;
    } catch (err) {
      state.writeError = toError(err);
    }
  }

  async function removeView(viewId: number): Promise<void> {
    try {
      const result = await deleteProjectView(state.config, state.config.projectId, viewId, state.config.repoFullName);
      state.views = result.views;
      state.writeError = null;
    } catch (err) {
      state.writeError = toError(err);
    }
  }

  // applyOptimistic applies a local mutation immediately and mirrors it with a write. mutate
  // returns its own revert so a failed write undoes exactly what it did.
  async function applyOptimistic(mutate: () => (() => void) | void, write: () => Promise<unknown>): Promise<void> {
    const revert = mutate();
    try {
      await write();
      state.writeError = null;
    } catch (err) {
      if (typeof revert === 'function') revert();
      state.writeError = toError(err);
    }
  }

  let refreshTimer: ReturnType<typeof setInterval> | null = null;
  let boardFingerprint = '';
  let roadmapFingerprint = '';

  // refresh re-fetches board and roadmap and only replaces state when the response actually
  // changed, so an unaffected view does not re-render on every tick.
  async function refresh(): Promise<void> {
    if (document.hidden) return;
    const [board, roadmap] = await Promise.allSettled([
      getBoard(state.config, {repoId: state.config.repoId, projectId: state.config.projectId}),
      getRoadmap(state.config, {repoId: state.config.repoId}),
    ]);
    let replaced = false;
    if (board.status === 'fulfilled') {
      const fp = JSON.stringify(board.value);
      if (fp !== boardFingerprint) {
        boardFingerprint = fp;
        state.board = board.value;
        replaced = true;
      }
      state.boardError = null;
    } else {
      state.boardError = toError(board.reason);
    }
    if (roadmap.status === 'fulfilled') {
      const fp = JSON.stringify(roadmap.value);
      if (fp !== roadmapFingerprint) {
        roadmapFingerprint = fp;
        state.roadmap = roadmap.value;
        replaced = true;
      }
      state.roadmapError = null;
    } else {
      state.roadmapError = toError(roadmap.reason);
    }
    // A replaced board or roadmap is authoritative; an inline edit's optimistic value would
    // otherwise keep overriding it, possibly with data someone else has since changed.
    if (replaced) state.overrides = {};
  }

  function startAutoRefresh(): void {
    stopAutoRefresh();
    refreshTimer = setInterval(refresh, REFRESH_INTERVAL_MS);
  }

  function stopAutoRefresh(): void {
    if (refreshTimer !== null) clearInterval(refreshTimer);
    refreshTimer = null;
  }

  return {
    state, loadAll, loadViews, saveView, removeView, applyOptimistic, setOverride,
    refresh, startAutoRefresh, stopAutoRefresh,
  };
}

export type PlanningStore = ReturnType<typeof createPlanningStore>;
