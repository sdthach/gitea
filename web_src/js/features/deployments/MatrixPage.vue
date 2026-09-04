<script lang="ts" setup>
import {computed, onMounted, reactive, ref} from 'vue';
import {
  ApiError, approveReview, findWaitingDeployment, getDeploymentChecks, getDeploymentHistory,
  getMatrix, getReleases, getReviews, rejectReview, saveToken,
} from './api.ts';
import type {DeploymentsApiConfig} from './api.ts';
import ErrorBanner from './ErrorBanner.vue';
import type {Check, Deployment, MatrixCell, MatrixRow, Review} from './types.ts';

const props = defineProps<{config: DeploymentsApiConfig}>();

const rows = ref<MatrixRow[]>([]);
const loaded = ref(false);
const errorMessage = ref('');
const errorAction = ref('');
const showTokenBox = ref(false);
const tokenInput = ref('');
const heldByRun = new Map<number, Review>();
const openHistory = reactive(new Map<string, {history: Deployment[]; artifacts: Array<{name: string; url: string}>}>());
const waitingChecks = reactive(new Map<string, Check[]>());

type Column = {environment: string; sort_order: number};

const columns = computed<Column[]>(() => {
  const found: Column[] = [];
  for (const row of rows.value) {
    for (const cell of row.cells) {
      if (!found.some((c) => c.environment === cell.environment)) found.push(cell);
    }
  }
  return found.sort((a, b) => a.sort_order - b.sort_order); // a stable sort leaves ties in first-seen order
});

function cellFor(row: MatrixRow, environment: string): MatrixCell | undefined {
  return row.cells.find((c) => c.environment === environment);
}

function cellHref(row: MatrixRow, cell: MatrixCell): string {
  return cell.run_url || `${props.config.appSubUrl}/${row.repo_full_name}/actions?workflow=deploy-${encodeURIComponent(cell.environment)}.yaml`;
}

// promoteURL is step one of two: a cell offers "deploy this release here", and the confirm
// page is where the target, the tag and the release currently live there are named. Nothing
// is dispatched by following it.
function promoteURL(row: MatrixRow, cell: MatrixCell): string {
  const query = new URLSearchParams({repo: row.repo_full_name, environment: cell.environment, release_tag: row.release_tag});
  return `${props.config.appSubUrl}/deployments/new?${query}`;
}

function reviewsLink(environment: string): string {
  return `${props.config.appSubUrl}/deployments/environments/${encodeURIComponent(environment)}/reviews`;
}

function held(cell: MatrixCell): Review | undefined {
  return heldByRun.get(cell.run_id);
}

function waitingKey(row: MatrixRow, cell: MatrixCell): string {
  return `${row.repo_id}:${cell.environment}:${row.release_tag}`;
}

function waitingNote(row: MatrixRow, cell: MatrixCell): string {
  const checks = waitingChecks.get(waitingKey(row, cell));
  if (!checks) return '';
  const pending = checks.filter((c) => c.state === 'wait');
  if (!pending.length) return '';
  return pending.map((c) => `${c.name}${c.retry_at ? `, retry at ${new Date(c.retry_at * 1000).toLocaleTimeString()}` : ''}`).join('; ');
}

// loadWaitingChecks resolves a `waiting` cell's own placeholder deployment id, then asks it
// what it is still waiting on. Neither the matrix nor the cell carries that id directly.
async function loadWaitingChecks(row: MatrixRow, cell: MatrixCell) {
  const key = waitingKey(row, cell);
  if (waitingChecks.has(key)) return;
  try {
    const found = await findWaitingDeployment(props.config, {repoId: row.repo_id, environment: cell.environment, releaseTag: row.release_tag});
    if (!found.length) return;
    const checks = await getDeploymentChecks(props.config, found[0].id);
    waitingChecks.set(key, checks);
  } catch {
    // A cell still renders its ⏳ symbol without the detail; the next sweep resolves it anyway.
  }
}

function historyKey(row: MatrixRow): string {
  return `${row.repo_id}:${row.release_tag}`;
}

async function toggleHistory(row: MatrixRow) {
  const key = historyKey(row);
  if (openHistory.has(key)) {
    openHistory.delete(key);
    return;
  }
  const [history, releases] = await Promise.all([
    getDeploymentHistory(props.config, {repoId: row.repo_id, releaseTag: row.release_tag}),
    getReleases(props.config, row.repo_full_name, row.release_tag),
  ]);
  openHistory.set(key, {history, artifacts: releases[0]?.artifacts ?? []});
}

function eventsFor(deployment: Deployment): string {
  const events = (deployment.audit ?? []).map((e) => `${e.event} by ${e.actor_login || 'unknown'}`).join(' → ');
  return `${deployment.environment}: ${events || deployment.status}`;
}

async function decideHeld(review: Review, verb: 'approve' | 'reject') {
  try {
    await (verb === 'approve' ? approveReview(props.config, review.id) : rejectReview(props.config, review.id));
    window.location.reload();
  } catch (err) {
    if (err instanceof ApiError) {
      if (err.status === 401 || err.status === 403) showTokenBox.value = true;
      errorMessage.value = err.message;
      errorAction.value = err.suggestedAction || `Open ${props.config.appSubUrl}/deployments/reviews, which can be given an API token.`;
    } else {
      errorMessage.value = String(err);
      errorAction.value = `Check that you are signed in and that the deployments API is reachable at ${props.config.apiBase}.`;
    }
  }
}

// heldByRun indexes the deploys the review gate is holding, so a `⏸` cell can offer the
// actions that release it. can_approve is the server's own answer, resolved by the same
// check the approve endpoint enforces, so no action is offered that would be refused.
async function loadReviews() {
  try {
    for (const review of await getReviews(props.config, {limit: 200})) {
      if (review.state === 'pending') heldByRun.set(review.run_id, review);
    }
  } catch {
    // The matrix still renders without them: the gate at job assignment, not this button, is
    // what withholds the job.
  }
}

async function load() {
  await loadReviews();
  try {
    rows.value = await getMatrix(props.config, 50);
    if (!rows.value.length) {
      errorMessage.value = 'no release is visible to you yet';
      errorAction.value = 'Cut a release, or check your account can read the Actions unit of a repository that has one.';
      return;
    }
    for (const row of rows.value) {
      for (const cell of row.cells) {
        if (cell.state === 'waiting') loadWaitingChecks(row, cell);
      }
    }
  } catch (err) {
    if (err instanceof ApiError) {
      errorMessage.value = err.message;
      errorAction.value = err.suggestedAction || 'Retry, and check the server log if it keeps failing.';
    } else {
      errorMessage.value = String(err);
      errorAction.value = `Check that you are signed in and that the deployments API is reachable at ${props.config.apiBase}.`;
    }
  } finally {
    loaded.value = true;
  }
}

function useToken() {
  if (!saveToken(tokenInput.value.trim())) {
    errorMessage.value = 'this browser refused to keep the token for this tab';
    errorAction.value = 'Allow site data for this origin, or use the gitea-deployments CLI instead.';
    return;
  }
  window.location.reload();
}

onMounted(load);
</script>

<template>
  <div>
    <h2 class="ui header">Deployments</h2>
    <p class="tw-text-14">
      Releases are rows, environments are columns in configured order. Every cell state is
      projected from the append-only audit log by
      <code>{{ config.apiBase }}/deployments/matrix</code>; this page is a client of that endpoint and reads
      nothing the API does not serve.
    </p>

    <ErrorBanner header="Could not load deployments" :message="errorMessage" :suggested-action="errorAction"/>

    <div id="deployments-token-box" class="ui warning message" :class="{'tw-hidden': !showTokenBox}">
      <div class="header">Approving from here needs an API token</div>
      <p>
        Reads use your browser session, but an approval never does, so that no other site
        holding your cookie can release a deploy. Create a token under Settings &gt;
        Applications and paste it here; it is kept in this tab only.
      </p>
      <div class="ui action input">
        <input id="deployments-token" v-model="tokenInput" type="password" autocomplete="off" placeholder="API token">
        <button id="deployments-token-save" class="ui primary button" type="button" @click="useToken">Use token</button>
      </div>
    </div>

    <table class="ui table" id="deployments-grid">
      <thead>
        <tr id="deployments-grid-head">
          <th>Repository</th>
          <th>Release</th>
          <th v-for="column in columns" :key="column.environment">
            <a :href="`${config.appSubUrl}/deployments/environments/${encodeURIComponent(column.environment)}`" title="Open this environment">{{ column.environment }}</a>
          </th>
        </tr>
      </thead>
      <tbody id="deployments-grid-body">
        <tr v-if="!loaded"><td :colspan="2 + columns.length">Loading…</td></tr>
        <template v-for="row in rows" :key="`${row.repo_id}:${row.release_tag}`">
          <tr>
            <td><a :href="`${config.appSubUrl}/${row.repo_full_name}`">{{ row.repo_full_name }}</a></td>
            <td>
              <a :href="row.release_url || `${config.appSubUrl}/${row.repo_full_name}/releases`">{{ row.release_tag }}</a>
              <button type="button" class="ui mini basic button" title="This release across every environment, from the audit log" @click="toggleHistory(row)">history</button>
            </td>
            <td v-for="column in columns" :key="column.environment">
              <template v-if="cellFor(row, column.environment)">
                <a :href="cellHref(row, cellFor(row, column.environment)!)" :title="`${row.release_tag} → ${column.environment}: ${cellFor(row, column.environment)!.state}`">{{ cellFor(row, column.environment)!.symbol }}</a>
                <a :href="promoteURL(row, cellFor(row, column.environment)!)" :title="`Deploy ${row.release_tag} to ${column.environment}`">▸</a>
                <template v-if="held(cellFor(row, column.environment)!)">
                  <a :href="reviewsLink(column.environment)" :title="`${row.release_tag} → ${column.environment} is held awaiting approval`">review</a>
                  <button v-if="held(cellFor(row, column.environment)!)!.can_approve" type="button" class="ui mini basic button" @click="decideHeld(held(cellFor(row, column.environment)!)!, 'approve')">approve</button>
                  <button v-if="held(cellFor(row, column.environment)!)!.can_approve" type="button" class="ui mini basic button" @click="decideHeld(held(cellFor(row, column.environment)!)!, 'reject')">reject</button>
                </template>
                <div v-if="waitingNote(row, cellFor(row, column.environment)!)" class="deployments-waiting-note tw-text-12">{{ waitingNote(row, cellFor(row, column.environment)!) }}</div>
              </template>
            </td>
          </tr>
          <tr v-if="openHistory.has(historyKey(row))">
            <td :colspan="2 + columns.length">
              <ul>
                <li v-if="!openHistory.get(historyKey(row))!.history.length">never deployed anywhere</li>
                <li v-for="deployment in openHistory.get(historyKey(row))!.history" :key="deployment.id">
                  {{ eventsFor(deployment) }}
                  <a v-if="deployment.run_url" :href="deployment.run_url">run</a>
                </li>
                <li v-if="openHistory.get(historyKey(row))!.artifacts.length">
                  artifacts:
                  <a v-for="artifact in openHistory.get(historyKey(row))!.artifacts" :key="artifact.url" :href="artifact.url">{{ artifact.name }}</a>
                </li>
              </ul>
            </td>
          </tr>
        </template>
      </tbody>
    </table>

    <p class="tw-text-12">
      <code>·</code> never deployed ·
      <code>✔ now</code> currently live ·
      <code>✔</code> superseded ·
      <code>✔ ×N</code> deployed here N times ·
      <code>✗</code> last attempt failed ·
      <code>⟳</code> in progress ·
      <code>⏸</code> awaiting approval
    </p>
  </div>
</template>
