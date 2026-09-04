<script lang="ts" setup>
import {computed, onMounted, reactive, ref} from 'vue';
import {ApiError, createEnvironment, getEnvironments, getRepoByFullName, getRepository, saveToken} from './api.ts';
import {checks, normalize, scopeLabel} from './environments.ts';
import ErrorBanner from './ErrorBanner.vue';
import PromotionPath from './PromotionPath.vue';
import type {EnvironmentsPageConfig} from './types.ts';
import type {Environment} from './types.ts';

const props = defineProps<{config: EnvironmentsPageConfig}>();

const environments = ref<Environment[]>([]);
const loaded = ref(false);
const errorMessage = ref('');
const errorAction = ref('');
const showTokenBox = ref(false);
const tokenInput = ref('');

const newName = ref('');
const newRepo = ref('');
const repoNames = reactive(new Map<number, string>());

function fail(err: unknown, fallback: string) {
  if (err instanceof ApiError) {
    if (err.status === 401 || err.status === 403) showTokenBox.value = true;
    errorMessage.value = err.message;
    errorAction.value = err.suggestedAction || fallback;
  } else {
    errorMessage.value = String(err);
    errorAction.value = fallback;
  }
}

async function scopeOf(env: Environment): Promise<string> {
  if (!env.repo_id) return 'instance-wide';
  if (!repoNames.has(env.repo_id)) {
    const repo = await getRepository(props.config, env.repo_id);
    repoNames.set(env.repo_id, scopeLabel(env.repo_id, repo?.full_name));
  }
  return repoNames.get(env.repo_id)!;
}

const presentChecks = computed(() => (env: Environment) => checks.filter((c) => c.present(normalize(env))));

// scopes is every distinct repository the loaded rows belong to, in the order first seen: one
// promotion path graph per scope, so a filtered-by-name view still shows the graph that name's
// own repository belongs to.
const scopes = computed(() => {
  const seen = new Map<number, string>();
  for (const env of environments.value) {
    if (!seen.has(env.repo_id)) seen.set(env.repo_id, repoNames.get(env.repo_id) ?? scopeLabel(env.repo_id, undefined));
  }
  return [...seen.entries()].map(([repoId, label]) => ({repoId, label}));
});

async function load() {
  try {
    environments.value = await getEnvironments(props.config, {name: props.config.name || undefined});
    for (const env of environments.value) await scopeOf(env); // populate repoNames before scopes recomputes
    errorMessage.value = '';
    showTokenBox.value = false;
  } catch (err) {
    fail(err, `Check that you are signed in and that the deployments API is reachable at ${props.config.apiBase}.`);
  } finally {
    loaded.value = true;
  }
}

function parseFullName(full: string): {owner: string; repo: string} | null {
  const cut = full.indexOf('/');
  if (cut < 1 || cut === full.length - 1 || full.includes('/', cut + 1)) return null;
  return {owner: full.slice(0, cut), repo: full.slice(cut + 1)};
}

async function create() {
  const name = newName.value.trim();
  if (!name) {
    errorMessage.value = 'an environment needs a name';
    errorAction.value = 'Type a name, then press New environment.';
    return;
  }
  const full = newRepo.value.trim();
  let repoID = 0;
  if (full) {
    const parsed = parseFullName(full);
    if (!parsed) {
      errorMessage.value = `"${full}" does not name a repository`;
      errorAction.value = 'Write the repository as owner/name, or leave it blank for an instance-wide environment.';
      return;
    }
    const repo = await getRepoByFullName(props.config, full);
    if (!repo) {
      errorMessage.value = `no repository ${full} is visible to you`;
      errorAction.value = 'Check the owner and repository name, or leave it blank for an instance-wide environment.';
      return;
    }
    repoID = repo.id;
  }
  try {
    const created = await createEnvironment(props.config, {
      repo_id: repoID, name, sort_order: 10, review_policy: 'none', required_reviewers: 1,
    });
    window.location.href = `${props.config.appSubUrl}/deployments/environments/${created.id}/edit`;
  } catch (err) {
    fail(err, 'Retry, and check the server log if it keeps failing.');
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
    <h2 class="ui header">Environments</h2>
    <p class="tw-text-14">
      Every figure below is fetched from <code>{{ config.apiBase }}/environments</code>. This
      page is a client of that endpoint and reads nothing the API does not serve.
    </p>

    <ErrorBanner header="Could not load environments" :message="errorMessage" :suggested-action="errorAction"/>

    <div id="deployments-token-box" class="ui warning message" :class="{'tw-hidden': !showTokenBox}">
      <div class="header">Creating an environment needs an API token</div>
      <p>
        Reads use your browser session, but a write never does. Create a token under Settings
        &gt; Applications and paste it here; it is kept in this tab only.
      </p>
      <div class="ui action input">
        <input id="deployments-token" v-model="tokenInput" type="password" autocomplete="off" placeholder="API token">
        <button id="deployments-token-save" class="ui primary button" type="button" @click="useToken">Use token</button>
      </div>
    </div>

    <div id="deployments-new" class="ui segment">
      <div class="ui form">
        <div class="inline fields">
          <div class="field">
            <label for="deployments-new-name">Environment name</label>
            <input id="deployments-new-name" v-model="newName" type="text" size="18" placeholder="staging">
          </div>
          <div class="field">
            <label for="deployments-new-repo">Repository</label>
            <input id="deployments-new-repo" v-model="newRepo" type="text" size="24" placeholder="owner/name — blank for instance-wide">
          </div>
          <div class="field">
            <button id="deployments-create" class="ui primary button" type="button" @click="create">New environment</button>
          </div>
        </div>
      </div>
    </div>

    <div v-for="scope in scopes" :key="scope.repoId">
      <PromotionPath :config="config" :repo-id="scope.repoId" :label="scope.label"/>
    </div>

    <table class="ui table" id="deployments-environments">
      <thead>
        <tr><th>Name</th><th>Scope</th><th>Order</th><th>Checks</th><th/></tr>
      </thead>
      <tbody id="deployments-environments-body">
        <tr v-if="!loaded"><td colspan="5">Loading…</td></tr>
        <tr v-else-if="!environments.length">
          <td colspan="5">{{ config.name ? `No environment named "${config.name}" is visible to you.` : 'No environment is configured yet.' }}</td>
        </tr>
        <tr v-for="env in environments" :key="env.id">
          <td><a :href="`${config.appSubUrl}/deployments/environments/${env.id}/edit`">{{ env.name }}</a></td>
          <td>{{ repoNames.get(env.repo_id) ?? scopeLabel(env.repo_id, undefined) }}</td>
          <td>{{ env.sort_order }}</td>
          <td>
            <span v-for="check in presentChecks(env)" :key="check.key" class="ui label">{{ check.label }}</span>
          </td>
          <td>
            <a :href="`${config.appSubUrl}/deployments`">deployments</a> ·
            <a :href="`${config.appSubUrl}/deployments/environments/${env.id}/edit`">settings</a>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
