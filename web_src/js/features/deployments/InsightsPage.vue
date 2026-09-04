<script lang="ts" setup>
import {onBeforeUnmount, onMounted, ref} from 'vue';
import {ApiError, getInsights, getInsightsRepos, getInsightsTrends, getRuns} from './api.ts';
import ErrorBanner from './ErrorBanner.vue';
import type {InsightsPageConfig} from './types.ts';
import type {Insights, RepoStat, Run, TrendPoint} from './types.ts';

const props = defineProps<{config: InsightsPageConfig}>();

// Gitea's own live-update transport is a per-user WebSocket carrying notification counts,
// stopwatches and logout, and nothing else. Publishing cross-repository run events onto it
// would need an entry in its client-side event allowlist — an upstream edit outside the
// fork's declared spoke set — so this page re-reads the same documented endpoints on an
// interval instead of opening a second transport of its own.
const refreshMillis = 30000;

const windowDays = ref(String(props.config.defaultWindowDays));
const errorMessage = ref('');
const errorAction = ref('');
const truncated = ref(false);
const updatedAt = ref('');
const loaded = ref(false);

const insights = ref<Insights | null>(null);
const recent = ref<Run[]>([]);
const failed = ref<Run[]>([]);
const repos = ref<RepoStat[]>([]);
const trends = ref<TrendPoint[]>([]);

function percent(rate: number | undefined): string {
  return `${Math.round((rate ?? 0) * 1000) / 10}%`;
}

function duration(seconds: number | undefined): string {
  const s = Math.max(0, Math.round(seconds ?? 0));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}

function runOf(summary: Insights['summary'], state: string): number {
  return summary.runs?.[state] ?? 0;
}

function runLabel(run: Run): string {
  const title = run.title ? `${run.repo_full_name} #${run.index}` : `${run.repo_full_name} #${run.index}`;
  return `${title} · ${run.workflow_id} · ${run.status} · ${duration(run.duration_seconds)}`;
}

function runHref(run: Run): string {
  return run.run_url || `${props.config.appSubUrl}/${run.repo_full_name}/actions`;
}

async function load() {
  try {
    const [overview, recentRuns, failedRuns, repoStats, trendPoints] = await Promise.all([
      getInsights(props.config, windowDays.value),
      getRuns(props.config, {limit: 10}),
      getRuns(props.config, {limit: 10, status: 'failure'}),
      getInsightsRepos(props.config, windowDays.value, 10),
      getInsightsTrends(props.config, windowDays.value),
    ]);
    errorMessage.value = '';
    insights.value = overview;
    truncated.value = overview.truncated;
    recent.value = recentRuns;
    failed.value = failedRuns;
    repos.value = repoStats;
    trends.value = trendPoints;
    updatedAt.value = `updated ${new Date().toLocaleTimeString()}`;
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

let timer: ReturnType<typeof setInterval> | undefined;
onMounted(() => {
  load();
  timer = setInterval(load, refreshMillis);
});
onBeforeUnmount(() => clearInterval(timer));
</script>

<template>
  <div>
    <h2 class="ui header">Insights</h2>
    <p class="tw-text-14">
      Every run across the repositories you can see. Gitea shows runs one repository at a time;
      this is the aggregate, served by <code>{{ config.apiBase }}/insights</code>. Every figure here
      is also reachable on its own from <code>/runs</code>, <code>/workflows</code> and
      <code>/insights/repos</code>, and every detail links out to Gitea's own page.
    </p>

    <div class="ui form">
      <div class="inline field">
        <label for="deployments-window">Window</label>
        <select id="deployments-window" v-model="windowDays" class="ui dropdown" @change="load">
          <option value="1">last day</option>
          <option :value="String(config.defaultWindowDays)">{{ `last ${config.defaultWindowDays} days` }}</option>
          <option value="30">last 30 days</option>
          <option value="90">last 90 days</option>
        </select>
        <button id="deployments-refresh" type="button" class="ui basic button" @click="load">Refresh now</button>
        <span id="deployments-updated" class="tw-text-12">{{ updatedAt }}</span>
      </div>
    </div>

    <ErrorBanner header="Could not load insights" :message="errorMessage" :suggested-action="errorAction"/>

    <div id="deployments-truncated" class="ui warning message" :class="{'tw-hidden': !truncated}">
      <div class="header">These numbers are a floor, not a total</div>
      <p>The window holds more runs than one aggregate reads.</p>
      <p><strong>Suggested action:</strong> narrow the window, or filter to one repository.</p>
    </div>

    <h3 class="ui header">Summary <span class="tw-text-12">(previous window of equal length beside each)</span></h3>
    <table class="ui table" id="deployments-tiles">
      <thead><tr><th>Tile</th><th>This window</th><th>Previous window</th></tr></thead>
      <tbody id="deployments-tiles-body">
        <tr v-if="!loaded"><td colspan="3">Loading…</td></tr>
        <template v-else-if="insights">
          <tr><td>Repositories active</td><td>{{ insights.summary.active_repositories }}</td><td>{{ insights.previous.active_repositories }}</td></tr>
          <tr><td>Repositories inactive</td><td>{{ insights.summary.inactive_repositories }}</td><td>{{ insights.previous.inactive_repositories }}</td></tr>
          <tr><td>Workflows active</td><td>{{ insights.summary.active_workflows }}</td><td>{{ insights.previous.active_workflows }}</td></tr>
          <tr><td>Workflows disabled</td><td>{{ insights.summary.disabled_workflows }}</td><td>{{ insights.previous.disabled_workflows }}</td></tr>
          <tr><td>Runs total</td><td>{{ insights.summary.total_runs }}</td><td>{{ insights.previous.total_runs }}</td></tr>
          <tr><td>Runs succeeded</td><td>{{ runOf(insights.summary, 'success') }}</td><td>{{ runOf(insights.previous, 'success') }}</td></tr>
          <tr><td>Runs failed</td><td>{{ runOf(insights.summary, 'failure') }}</td><td>{{ runOf(insights.previous, 'failure') }}</td></tr>
          <tr><td>Runs in progress</td><td>{{ runOf(insights.summary, 'in_progress') }}</td><td>{{ runOf(insights.previous, 'in_progress') }}</td></tr>
          <tr><td>Runs queued</td><td>{{ runOf(insights.summary, 'queued') }}</td><td>{{ runOf(insights.previous, 'queued') }}</td></tr>
          <tr><td>Runs cancelled</td><td>{{ runOf(insights.summary, 'cancelled') }}</td><td>{{ runOf(insights.previous, 'cancelled') }}</td></tr>
          <tr><td>Success rate</td><td>{{ percent(insights.summary.success_rate) }}</td><td>{{ percent(insights.previous.success_rate) }}</td></tr>
          <tr><td>Total duration</td><td>{{ duration(insights.summary.total_duration_seconds) }}</td><td>{{ duration(insights.previous.total_duration_seconds) }}</td></tr>
        </template>
      </tbody>
    </table>

    <div class="ui two column stackable grid">
      <div class="column">
        <h3 class="ui header">Recent runs</h3>
        <table class="ui table">
          <tbody id="deployments-recent">
            <tr v-if="!loaded"><td>Loading…</td></tr>
            <tr v-else-if="!recent.length"><td>no run in this window</td></tr>
            <template v-else><tr v-for="run in recent" :key="run.id"><td><a :href="runHref(run)" :title="run.title">{{ runLabel(run) }}</a></td></tr></template>
          </tbody>
        </table>
      </div>
      <div class="column">
        <h3 class="ui header">Failed runs</h3>
        <table class="ui table">
          <tbody id="deployments-failed">
            <tr v-if="!loaded"><td>Loading…</td></tr>
            <tr v-else-if="!failed.length"><td>no failed run — nothing to chase</td></tr>
            <template v-else><tr v-for="run in failed" :key="run.id"><td><a :href="runHref(run)" :title="run.title">{{ runLabel(run) }}</a></td></tr></template>
          </tbody>
        </table>
      </div>
    </div>

    <h3 class="ui header">Top repositories by run volume</h3>
    <table class="ui table">
      <thead><tr><th>Repository</th><th>Runs</th><th>Success rate</th><th>Average duration</th></tr></thead>
      <tbody id="deployments-repos">
        <tr v-if="!loaded"><td colspan="4">Loading…</td></tr>
        <tr v-else-if="!repos.length"><td>no repository ran a workflow in this window</td><td/><td/><td/></tr>
        <template v-else>
          <tr v-for="repo in repos" :key="repo.repo_id">
            <td><a :href="`${config.appSubUrl}/${repo.repo_full_name}/actions`" title="Open this repository's runs in Gitea">{{ repo.repo_full_name }}</a></td>
            <td>{{ repo.runs }}</td>
            <td>{{ percent(repo.success_rate) }}</td>
            <td>{{ duration(repo.average_duration_seconds) }}</td>
          </tr>
        </template>
      </tbody>
    </table>

    <h3 class="ui header">Daily trend</h3>
    <table class="ui table">
      <thead><tr><th>Day</th><th>Runs</th><th>Successful</th><th>Failed</th><th>Average duration</th><th>Deployments</th></tr></thead>
      <tbody id="deployments-trend">
        <tr v-if="!loaded"><td colspan="6">Loading…</td></tr>
        <template v-else>
          <tr v-for="point in trends" :key="point.date">
            <td>{{ point.date }}</td><td>{{ point.runs }}</td><td>{{ point.successes }}</td>
            <td>{{ point.failures }}</td><td>{{ duration(point.average_duration_seconds) }}</td><td>{{ point.deployments }}</td>
          </tr>
        </template>
      </tbody>
    </table>
  </div>
</template>
