<script lang="ts" setup>
import {computed, ref} from 'vue';
import {ApiError} from '../../api.ts';
import type {PlanningApiConfig, PlanningScope} from '../../api.ts';
import type {PlanningSettingsConfig} from '../../types.ts';
import TypesTab from './TypesTab.vue';
import FieldsTab from './FieldsTab.vue';
import CapacityTab from './CapacityTab.vue';

const props = defineProps<{config: PlanningSettingsConfig}>();

const tabs = ['types', 'fields', 'capacity'] as const;
const activeTab = ref<typeof tabs[number]>('types');

const apiConfig = computed<PlanningApiConfig>(() => ({apiBase: props.config.apiBase, token: props.config.token}));
const scope = computed<PlanningScope>(() => (props.config.repoId ? {repoId: props.config.repoId} : props.config.orgId ? {orgId: props.config.orgId} : {}));
// canWrite reads false with no token even when the doer otherwise could: a token the page
// failed to mint carries no write scope, so every editor is disabled rather than failing.
const canWrite = computed(() => props.config.canWrite && !!props.config.token);
const title = computed(() => props.config.repoFullName || (props.config.orgId ? props.config.ownerName : 'Instance'));
const projectsHref = computed(() => `${window.config.appSubUrl}/planning/projects`);

const error = ref<{message: string; suggestedAction: string} | null>(null);
function onError(err: unknown) {
  error.value = err instanceof ApiError ? {message: err.message, suggestedAction: err.suggestedAction} : {message: String(err), suggestedAction: ''};
}
</script>

<template>
  <div class="tw-flex tw-flex-col tw-gap-4">
    <div class="tw-flex tw-items-center tw-justify-between tw-flex-wrap tw-gap-2">
      <h2 class="ui header">{{ title }} planning settings</h2>
      <a :href="projectsHref">Back to projects</a>
    </div>

    <div v-if="!config.token" class="ui negative message">
      <p>The page could not get a write token, so editing is turned off.</p>
      <p><strong>Suggested action:</strong> Reload the page.</p>
    </div>
    <div v-else-if="error" class="ui negative message">
      <p>{{ error.message }}</p>
      <p v-if="error.suggestedAction"><strong>Suggested action:</strong> {{ error.suggestedAction }}</p>
    </div>

    <div class="ui secondary pointing menu">
      <a v-for="tab in tabs" :key="tab" class="item" :class="{active: activeTab === tab}" @click="activeTab = tab">{{ tab }}</a>
    </div>

    <TypesTab v-if="activeTab === 'types'" :config="apiConfig" :scope="scope" :can-write="canWrite" @error="onError"/>
    <FieldsTab v-else-if="activeTab === 'fields'" :config="apiConfig" :scope="scope" :can-write="canWrite" @error="onError"/>
    <CapacityTab v-else-if="activeTab === 'capacity'" :config="apiConfig" :scope="scope" :can-write="canWrite" @error="onError"/>
  </div>
</template>
