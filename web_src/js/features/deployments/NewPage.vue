<script lang="ts" setup>
import {computed, onMounted, ref} from 'vue';
import {planOrConfirmDeployment, saveToken} from './api.ts';
import type {DeploymentsApiConfig} from './api.ts';
import ErrorBanner from './ErrorBanner.vue';
import type {Promotion} from './types.ts';

const props = defineProps<{config: DeploymentsApiConfig}>();

const params = new URLSearchParams(window.location.search);
const target = {
  repo: params.get('repo') ?? '',
  environment: params.get('environment') ?? '',
  release_tag: params.get('release_tag') ?? '',
};

const errorHeader = ref('Could not plan this deploy');
const errorMessage = ref('');
const errorAction = ref('');
const warningMessage = ref('');
const warningAction = ref('');
const showTokenBox = ref(false);
const plan = ref<Promotion | null>(null);
const overrideReason = ref('');
const confirmEnabled = ref(false);
const confirming = ref(false);
const doneMessage = ref('');
const doneRunUrl = ref('');

const planLine = computed(() => {
  if (!plan.value) return '…';
  return plan.value.release_tag
    + (plan.value.is_prerelease ? ' (prerelease)' : '')
    + (plan.value.is_rollback ? ' — this is a rollback' : '');
});
const dependsOnLine = computed(() => {
  if (!plan.value) return '…';
  return plan.value.depends_on?.length
    ? `${plan.value.depends_on.join(', ')} — ${plan.value.predecessor_state}`
    : 'none declared';
});
const workflowLine = computed(() => plan.value ? `${plan.value.workflow_id} at ${plan.value.ref}` : '…');
const confirmLabel = computed(() => plan.value?.requires_override_reason ? 'Override and deploy' : 'Deploy');

function fail(header: string, message: string, action: string) {
  errorHeader.value = header;
  errorMessage.value = message;
  errorAction.value = action;
}

// post is the single request both steps compose. Only `confirm` differs, so nothing the
// confirm step showed can drift from what the dispatch actually asks for.
async function post(confirm: boolean, overrideReasonValue: string) {
  const body: Record<string, unknown> = {...target, confirm};
  if (overrideReasonValue) body.override_reason = overrideReasonValue;
  const {status, payload} = await planOrConfirmDeployment(props.config, body as never);
  if (status === 401 || status === 403) showTokenBox.value = true;
  return {status, payload};
}

function renderPlan(p: Promotion) {
  plan.value = p;
  if (p.outcome === 'refuse') {
    fail('This deploy is refused', p.message ?? '', p.suggested_action ?? '');
    return;
  }
  if (p.outcome === 'warn' || p.outcome === 'override') {
    warningMessage.value = p.message ?? '';
    warningAction.value = p.suggested_action ?? '';
  }
  confirmEnabled.value = true;
}

async function onConfirm() {
  confirmEnabled.value = false;
  confirming.value = true;
  try {
    const {status, payload} = await post(true, overrideReason.value.trim());
    if (status >= 400 || !payload.confirmed) {
      fail('Not dispatched', payload.message ?? `the API returned ${status}`,
        payload.suggested_action ?? 'Retry, and check the server log if it keeps failing.');
      confirmEnabled.value = true;
      return;
    }
    doneMessage.value = `${payload.release_tag} is deploying to ${payload.environment}. Run ${payload.run_id}.`;
    doneRunUrl.value = payload.run_url || `${props.config.appSubUrl}/${payload.repo_full_name}/actions`;
  } catch (err) {
    fail('Not dispatched', String(err),
      `Check that you are signed in and that the deployments API is reachable at ${props.config.apiBase}.`);
    confirmEnabled.value = true;
  } finally {
    confirming.value = false;
  }
}

function useToken(tokenValue: string) {
  if (!saveToken(tokenValue.trim())) {
    fail('Could not keep the token', 'this browser refused to keep the token for this tab',
      'Allow site data for this origin, or use the gitea-deployments CLI instead.');
    return;
  }
  window.location.reload();
}
const tokenInput = ref('');

onMounted(async () => {
  if (!target.repo || !target.environment || !target.release_tag) {
    fail('Nothing to confirm', 'the link carries no repo, environment and release_tag',
      'Open this page from a matrix cell, which fills all three in.');
    return;
  }
  try {
    const {status, payload} = await post(false, '');
    // A 403 is normally the sequence rule refusing, which renders as a plan. An auth 403
    // carries no plan at all, so it must not reach renderPlan.
    if (status >= 400 && (status !== 403 || payload.code === 'sign_in_required')) {
      fail('Could not plan this deploy', payload.message ?? `the API returned ${status}`,
        payload.suggested_action ?? 'Retry, and check the server log if it keeps failing.');
    } else {
      renderPlan(payload);
    }
  } catch (err) {
    fail('Could not plan this deploy', String(err),
      `Check that you are signed in and that the deployments API is reachable at ${props.config.apiBase}.`);
  }
});
</script>

<template>
  <div>
    <h2 class="ui header">New deployment</h2>
    <p class="tw-text-14">
      Deploying is two steps. This is the second: nothing has been dispatched yet.
      Both steps are the same request to <code>POST {{ config.apiBase }}/deployments</code> —
      the first with <code>confirm: false</code>, which plans and dispatches nothing. Rolling
      back is this page with an earlier release tag; there is no separate rollback path.
    </p>

    <ErrorBanner :header="errorHeader" :message="errorMessage" :suggested-action="errorAction"/>

    <div id="deployments-token-box" class="ui warning message" :class="{'tw-hidden': !showTokenBox}">
      <div class="header">This page needs an API token</div>
      <p>
        Planning and dispatching a deploy authenticate with a token, never with your browser
        session, so that no other site holding your cookie can release one. Create a token
        under Settings &gt; Applications and paste it here; it is kept in this tab only.
      </p>
      <div class="ui action input">
        <input id="deployments-token" v-model="tokenInput" type="password" autocomplete="off" placeholder="API token">
        <button id="deployments-token-save" class="ui primary button" type="button" @click="useToken(tokenInput)">Use token</button>
      </div>
    </div>

    <div id="deployments-warning" class="ui warning message" :class="{'tw-hidden': !warningMessage}">
      <div class="header">Out of sequence</div>
      <p>{{ warningMessage }}</p>
      <p><strong>Suggested action:</strong> {{ warningAction }}</p>
    </div>

    <table class="ui definition table" id="deployments-plan">
      <tbody>
        <tr><td>Repository</td><td id="plan-repo">{{ plan?.repo_full_name ?? '…' }}</td></tr>
        <tr><td>Target environment</td><td id="plan-environment">{{ plan?.environment ?? '…' }}</td></tr>
        <tr><td>Release to deploy</td><td id="plan-release">{{ planLine }}</td></tr>
        <tr><td>Currently live there</td><td id="plan-live">{{ plan ? (plan.currently_live || 'nothing has ever succeeded here') : '…' }}</td></tr>
        <tr><td>Depends on</td><td id="plan-depends-on">{{ dependsOnLine }}</td></tr>
        <tr><td>Workflow</td><td id="plan-workflow">{{ workflowLine }}</td></tr>
      </tbody>
    </table>

    <div id="deployments-override" :class="{'tw-hidden': !plan?.requires_override_reason}">
      <p class="tw-text-14">
        <label for="override-reason"><strong>Reason for overriding the sequence rule</strong></label><br>
        It is written to the append-only audit log against your account.
      </p>
      <textarea id="override-reason" v-model="overrideReason" rows="2" class="tw-w-full"/>
    </div>

    <div class="tw-mt-3">
      <button id="deployments-confirm" class="ui primary button" :disabled="!confirmEnabled || confirming" @click="onConfirm">{{ confirmLabel }}</button>
      <a class="ui basic button" :href="`${config.appSubUrl}/deployments`">Cancel</a>
    </div>

    <div id="deployments-done" class="ui positive message" :class="{'tw-hidden': !doneMessage}">
      <div class="header">Dispatched</div>
      <p id="deployments-done-message">{{ doneMessage }} <a v-if="doneRunUrl" :href="doneRunUrl">Open the run</a></p>
    </div>
  </div>
</template>
