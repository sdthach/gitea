<script lang="ts" setup>
import {computed, onMounted, ref} from 'vue';
import {
  ApiError, deleteEnvironment, getActionsSecretNames, getEnvironment, getEnvironments,
  getRepoEnvironmentSecrets, getRepository, createSecretScope, deleteSecretScope, saveToken, updateEnvironment,
} from './api.ts';
import {normalize, payloadOf} from './environments.ts';
import EnvironmentPage from './EnvironmentPage.vue';
import ErrorBanner from './ErrorBanner.vue';
import type {DeploymentsApiConfig} from './api.ts';
import type {EnvironmentEditPageConfig, Environment, GiteaRepo, SecretName} from './types.ts';

const props = defineProps<{config: EnvironmentEditPageConfig}>();

const apiConfig = computed<DeploymentsApiConfig>(() => props.config);

const env = ref<Environment | null>(null);
const repo = ref<GiteaRepo | null>(null);
const siblings = ref<Environment[]>([]);
const secrets = ref<SecretName[] | null>(null);
const availableSecrets = ref<Array<{name: string}> | null>(null);
const loaded = ref(false);
const errorMessage = ref('');
const errorAction = ref('');
const showTokenBox = ref(false);
const tokenInput = ref('');

const nameInput = ref('');
const orderInput = ref('0');
const newSecretName = ref('');
const deleteConfirm = ref('');

const reviewsHref = computed(() => env.value
  ? `${props.config.appSubUrl}/deployments/environments/${encodeURIComponent(env.value.name)}/reviews`
  : `${props.config.appSubUrl}/deployments/reviews`);

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

async function loadSiblings() {
  if (!env.value) return;
  siblings.value = await getEnvironments(apiConfig.value, {repoId: env.value.repo_id, limit: 200});
}

async function loadSecrets() {
  if (!repo.value || !env.value) {
    secrets.value = null;
    return;
  }
  secrets.value = await getRepoEnvironmentSecrets(apiConfig.value, repo.value.full_name, env.value.name);
}

async function applyUpdate(next: Environment): Promise<boolean> {
  try {
    const updated = await updateEnvironment(apiConfig.value, props.config.environmentId, payloadOf(next));
    env.value = normalize(updated);
    nameInput.value = env.value.name;
    orderInput.value = String(env.value.sort_order);
    await Promise.all([loadSiblings(), loadSecrets()]); // a rename moves the secrets path
    errorMessage.value = '';
    showTokenBox.value = false;
    return true;
  } catch (err) {
    fail(err, 'Retry, and check the server log if it keeps failing.');
    return false;
  }
}

function saveIdentity() {
  if (!env.value) return;
  void applyUpdate({...env.value, name: nameInput.value.trim(), sort_order: Number(orderInput.value)});
}

function onCheckSave(draft: Environment) {
  void applyUpdate(draft);
}

async function bindSecret() {
  if (!env.value) return;
  const name = newSecretName.value.trim();
  if (!name) return;
  try {
    await createSecretScope(apiConfig.value, {repo_id: env.value.repo_id, secret_name: name, environment: env.value.name});
    newSecretName.value = '';
    await loadSecrets();
    errorMessage.value = '';
  } catch (err) {
    fail(err, 'Retry, and check the server log if it keeps failing.');
  }
}

async function unbindSecret(id: number) {
  try {
    await deleteSecretScope(apiConfig.value, id);
    await loadSecrets();
  } catch (err) {
    fail(err, 'Retry, and check the server log if it keeps failing.');
  }
}

async function removeEnvironment() {
  if (!env.value || deleteConfirm.value !== env.value.name) return;
  try {
    await deleteEnvironment(apiConfig.value, props.config.environmentId);
    window.location.href = `${props.config.appSubUrl}/deployments/environments`;
  } catch (err) {
    fail(err, 'Retry, and check the server log if it keeps failing.');
  }
}

const boundNames = computed(() => new Set((secrets.value ?? []).map((s) => s.name)));
const freeSecretNames = computed(() => (availableSecrets.value ?? []).filter((s) => !boundNames.value.has(s.name)));

function useToken() {
  if (!saveToken(tokenInput.value.trim())) {
    errorMessage.value = 'this browser refused to keep the token for this tab';
    errorAction.value = 'Allow site data for this origin, or use the gitea-deployments CLI instead.';
    return;
  }
  load();
}

async function load() {
  try {
    const payload = await getEnvironment(apiConfig.value, props.config.environmentId);
    env.value = normalize(payload);
    nameInput.value = env.value.name;
    orderInput.value = String(env.value.sort_order);
    if (env.value.repo_id) repo.value = await getRepository(apiConfig.value, env.value.repo_id);
    await Promise.all([loadSiblings(), loadSecrets()]);
    if (repo.value) availableSecrets.value = await getActionsSecretNames(apiConfig.value, repo.value.full_name);
    errorMessage.value = '';
    showTokenBox.value = false;
  } catch (err) {
    fail(err, `Check that you are signed in and that the deployments API is reachable at ${props.config.apiBase}.`);
  } finally {
    loaded.value = true;
  }
}

onMounted(load);
</script>

<template>
  <div>
    <h2 class="ui header" id="deployments-heading">
      {{ env?.name ?? 'Environment' }}
      <div v-if="env" class="sub header">{{ env.repo_id ? (repo?.full_name ?? `repository ${env.repo_id}`) : 'instance-wide' }}</div>
    </h2>
    <p class="tw-text-14">
      Every figure below is fetched from <code>{{ config.apiBase }}/environments</code>. This
      page is a client of that endpoint and reads nothing the API does not serve. A secret is
      listed by name; no endpoint returns a value.
    </p>

    <p class="tw-text-14">
      <a id="deployments-matrix-link" :href="`${config.appSubUrl}/deployments`">Deployments to this environment</a>
      ·
      <a id="deployments-reviews-link" :href="reviewsHref">Pending reviews</a>
    </p>

    <ErrorBanner header="Could not load this environment" :message="errorMessage" :suggested-action="errorAction"/>

    <div id="deployments-token-box" class="ui warning message" :class="{'tw-hidden': !showTokenBox}">
      <div class="header">Editing from here needs an API token</div>
      <p>
        Reads use your browser session, but a write never does, so that no other site holding
        your cookie can reconfigure a gate. Create a token under Settings &gt; Applications and
        paste it here; it is kept in this tab only.
      </p>
      <div class="ui action input">
        <input id="deployments-token" v-model="tokenInput" type="password" autocomplete="off" placeholder="API token">
        <button id="deployments-token-save" class="ui primary button" type="button" @click="useToken">Use token</button>
      </div>
    </div>

    <div id="deployments-detail" :class="{'tw-hidden': !loaded || !env}">
      <template v-if="env">
        <div class="ui form">
          <div class="inline fields">
            <div class="field">
              <label for="deployments-name">Name</label>
              <input id="deployments-name" v-model="nameInput" type="text" size="20" :readonly="!env.can_write">
            </div>
            <div class="field">
              <label for="deployments-order">Order</label>
              <input id="deployments-order" v-model="orderInput" type="number" size="6" :readonly="!env.can_write">
            </div>
            <div class="field" id="deployments-identity-actions">
              <button v-if="env.can_write" type="button" class="ui primary button" @click="saveIdentity">Save</button>
            </div>
          </div>
        </div>

        <h3 class="ui header">Checks</h3>
        <EnvironmentPage
          :config="apiConfig" :env="env" :can-write="env.can_write" :siblings="siblings"
          :owner-login="repo?.owner.login ?? ''" @save="onCheckSave"
        />

        <div id="deployments-secrets-section" :class="{'tw-hidden': !repo || secrets === null}">
          <h3 class="ui header">Secrets</h3>
          <div id="deployments-secrets">
            <div v-for="secret in secrets ?? []" :key="secret.id" class="tw-flex tw-items-center tw-gap-2">
              <strong>{{ secret.name }}</strong>
              <span class="tw-text-12">
                {{ secret.exists ? `only jobs declaring \`${env.name}\` see it` : 'bound, but no secret of this name exists in the repository' }}
              </span>
              <button v-if="env.can_write" type="button" class="ui basic mini button" @click="unbindSecret(secret.id)">Unbind</button>
            </div>
            <p v-if="secrets && !secrets.length" class="tw-text-14">No secret is scoped to this environment.</p>
          </div>
          <div v-if="env.can_write" class="ui form" id="deployments-bind-form">
            <div class="inline fields">
              <div class="field">
                <label for="deployments-secret-name">Secret</label>
                <select v-if="availableSecrets" id="deployments-secret-name" v-model="newSecretName">
                  <option v-for="secret in freeSecretNames" :key="secret.name" :value="secret.name">{{ secret.name }}</option>
                </select>
                <input v-else id="deployments-secret-name" v-model="newSecretName" type="text" placeholder="DEPLOY_KEY">
              </div>
              <div class="field">
                <button type="button" class="ui basic button" @click="bindSecret">Bind a secret</button>
              </div>
            </div>
          </div>
        </div>

        <div v-if="env.can_write" id="deployments-danger">
          <h3 class="ui header">Danger zone</h3>
          <div class="ui form" id="deployments-danger-form">
            <div class="inline fields">
              <div class="field">
                <label for="deployments-delete-confirm">{{ `Type ${env.name} to confirm` }}</label>
                <input id="deployments-delete-confirm" v-model="deleteConfirm" type="text">
              </div>
              <div class="field">
                <button
                  type="button" class="ui red button" :disabled="deleteConfirm !== env.name"
                  @click="removeEnvironment"
                >
                  Delete this environment
                </button>
              </div>
            </div>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>
