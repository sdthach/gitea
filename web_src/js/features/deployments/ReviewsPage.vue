<script lang="ts" setup>
import {onMounted, reactive, ref} from 'vue';
import {ApiError, approveReview, getDeploymentChecks, getReviews, getWaitingDeployments, rejectReview, saveToken} from './api.ts';
import ErrorBanner from './ErrorBanner.vue';
import type {ReviewsPageConfig} from './types.ts';
import type {Check, Deployment, Review} from './types.ts';

const props = defineProps<{config: ReviewsPageConfig}>();

const reviews = ref<Review[]>([]);
const loaded = ref(false);
const errorMessage = ref('');
const errorAction = ref('');
const showTokenBox = ref(false);
const tokenInput = ref('');

const waiting = ref<Deployment[]>([]);
const waitingChecks = reactive(new Map<number, Check[]>());

function age(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  return `${Math.floor(seconds / 86400)}d`;
}

function runHref(runUrl: string): string {
  return runUrl.startsWith('http') ? runUrl : `${props.config.appSubUrl}${runUrl}`;
}

// decide posts one review or rejection. The endpoint is the ONLY thing that releases a held
// job, so a refusal here is the real answer and is shown with the action it suggests.
async function decide(review: Review, verb: 'approve' | 'reject') {
  errorMessage.value = '';
  try {
    await (verb === 'approve' ? approveReview(props.config, review.id) : rejectReview(props.config, review.id));
    await load();
  } catch (err) {
    if (err instanceof ApiError) {
      if (err.status === 401 || err.status === 403) showTokenBox.value = true;
      errorMessage.value = err.message;
      errorAction.value = err.suggestedAction || 'Retry, and check the server log if it keeps failing.';
    } else {
      errorMessage.value = String(err);
      errorAction.value = `Check that you are signed in and that the deployments API is reachable at ${props.config.apiBase}.`;
    }
  }
}

function pendingChecks(deploymentId: number): Check[] {
  return (waitingChecks.get(deploymentId) ?? []).filter((c) => c.state === 'wait');
}

async function loadWaitingChecks() {
  for (const deployment of waiting.value) {
    if (waitingChecks.has(deployment.id)) continue;
    try {
      waitingChecks.set(deployment.id, await getDeploymentChecks(props.config, deployment.id));
    } catch {
      // The row still names the release and environment without the check detail.
    }
  }
}

async function load() {
  try {
    reviews.value = await getReviews(props.config, {environment: props.config.environmentName, limit: 200});
    waiting.value = await getWaitingDeployments(props.config, {environment: props.config.environmentName, limit: 200});
    errorMessage.value = '';
    showTokenBox.value = false;
    loaded.value = true;
    await loadWaitingChecks();
  } catch (err) {
    loaded.value = true;
    if (err instanceof ApiError) {
      if (err.status === 401 || err.status === 403) showTokenBox.value = true;
      errorMessage.value = err.message;
      errorAction.value = err.suggestedAction || 'Retry, and check the server log if it keeps failing.';
    } else {
      errorMessage.value = String(err);
      errorAction.value = `Check that you are signed in and that the deployments API is reachable at ${props.config.apiBase}.`;
    }
  }
}

function useToken() {
  if (!saveToken(tokenInput.value.trim())) {
    errorMessage.value = 'this browser refused to keep the token for this tab';
    errorAction.value = 'Allow site data for this origin, or use the gitea-deployments CLI instead.';
    return;
  }
  load();
}

onMounted(load);
</script>

<template>
  <div>
    <h2 class="ui header">Pending reviews</h2>
    <p class="tw-text-14">
      Every deploy the review gate is holding, with who asked for it, the release, how long it
      has waited and a link to the run. Every figure is fetched from
      <code>{{ config.apiBase }}/reviews</code>; this page is a client of that endpoint and
      reads nothing the API does not serve. Approving or rejecting calls that same endpoint, so
      a user the forge does not permit to approve is refused by the server, not merely offered
      no button.
    </p>

    <ErrorBanner header="Could not load pending reviews" :message="errorMessage" :suggested-action="errorAction"/>

    <div id="deployments-token-box" class="ui warning message" :class="{'tw-hidden': !showTokenBox}">
      <div class="header">This page needs an API token</div>
      <p>
        The deployments API authenticates with a token, not with your browser session, so that no
        review can be triggered by another site holding your cookie. Create one under
        Settings &gt; Applications and paste it here; it is kept in this tab only.
      </p>
      <div class="ui action input">
        <input id="deployments-token" v-model="tokenInput" type="password" autocomplete="off" placeholder="API token">
        <button id="deployments-token-save" class="ui primary button" type="button" @click="useToken">Use token</button>
      </div>
    </div>

    <table class="ui table" id="deployments-approvals">
      <thead>
        <tr>
          <th>Repository</th><th>Environment</th><th>Release</th><th>Requested by</th>
          <th>Age</th><th>State</th><th>Reviews</th><th>Run</th><th>Actions</th>
        </tr>
      </thead>
      <tbody id="deployments-approvals-body">
        <tr v-if="!loaded"><td colspan="9">Loading…</td></tr>
        <tr v-else-if="!reviews.length">
          <td colspan="9">{{ config.environmentName ? `nothing is awaiting review in "${config.environmentName}"` : 'nothing is awaiting review' }}</td>
        </tr>
        <tr v-for="review in reviews" :key="review.id">
          <td>{{ `#${review.repo_id}` }}</td>
          <td>{{ review.environment }}</td>
          <td>{{ review.release_tag || '—' }}</td>
          <td>{{ review.requester_login || `#${review.requester_id}` }}</td>
          <td>{{ age(review.age_seconds) }}</td>
          <td>{{ review.state }}</td>
          <td>{{ `${review.reviews_count}/${review.required_reviewers}` }}</td>
          <td><a v-if="review.run_url" :href="runHref(review.run_url)">{{ `run ${review.run_id}` }}</a><template v-else>{{ `run ${review.run_id}` }}</template></td>
          <td>
            <template v-if="review.can_approve">
              <button type="button" class="ui mini primary button" @click="decide(review, 'approve')">approve</button>
              <button type="button" class="ui mini negative button" @click="decide(review, 'reject')">reject</button>
            </template>
            <template v-else>{{ review.state === 'pending' ? 'not yours to review' : '—' }}</template>
          </td>
        </tr>
      </tbody>
    </table>

    <div v-if="loaded && waiting.length" id="deployments-waiting">
      <h3 class="ui header">Waiting on checks</h3>
      <table class="ui table">
        <thead><tr><th>Repository</th><th>Environment</th><th>Release</th><th>Waiting on</th></tr></thead>
        <tbody>
          <tr v-for="deployment in waiting" :key="deployment.id">
            <td>{{ `#${deployment.repo_id}` }}</td>
            <td>{{ deployment.environment }}</td>
            <td>{{ deployment.release_tag || '—' }}</td>
            <td>
              <template v-if="pendingChecks(deployment.id).length">
                <span v-for="check in pendingChecks(deployment.id)" :key="check.name" class="tw-text-12">
                  {{ check.name }}{{ check.retry_at ? `, retry at ${new Date(check.retry_at * 1000).toLocaleTimeString()}` : '' }}
                </span>
              </template>
              <span v-else>…</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <p class="tw-text-12">
      A rejection ends the deploy: the run does not proceed later. Both decisions are written to
      the append-only audit log naming the reviewer and the time.
    </p>
  </div>
</template>
