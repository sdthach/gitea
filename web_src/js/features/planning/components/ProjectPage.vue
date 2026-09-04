<script lang="ts" setup>
import {computed, onMounted, onUnmounted, reactive, watch} from 'vue';
import {createPlanningStore} from '../store.ts';
import {applyUrlState, buildSearch, parseUrlState} from '../url.ts';
import type {UrlState} from '../url.ts';
import type {Grouping} from '../groups.ts';
import type {PlanningProjectConfig} from '../types.ts';
import ProjectPicker from './ProjectPicker.vue';
import FilterBar from './FilterBar.vue';
import SavedViews from './SavedViews.vue';
import TableView from './TableView.vue';
import BoardView from './BoardView.vue';
import RoadmapView from './roadmap/RoadmapView.vue';
import TimeView from './time/TimeView.vue';

const props = defineProps<{config: PlanningProjectConfig}>();

const tabs = ['table', 'board', 'roadmap', 'time'];
const groupings: Grouping[] = ['none', 'type', 'assignee', 'parent'];

const store = createPlanningStore(props.config);
const urlState = reactive<UrlState>(parseUrlState(window.location.search));

watch(urlState, (state) => applyUrlState(state), {deep: true});

const groupBy = computed<Grouping>(() => (groupings.includes(urlState.group_by as Grouping) ? urlState.group_by as Grouping : 'none'));
const currentSearch = computed(() => buildSearch(urlState).replace(/^\?/, ''));
const errorBanner = computed(() => store.state.writeError ?? store.state.boardError ?? store.state.roadmapError ?? store.state.capacityError ?? store.state.timesheetError ?? store.state.viewsError);
// canWrite reads false with no token even when the doer otherwise could: a token the page
// failed to mint carries no write scope, so every editor is disabled rather than failing on
// first use.
const canWrite = computed(() => props.config.canWrite && !!props.config.token);
const canEditIssues = computed(() => props.config.canEditIssues && !!props.config.token);

function applySavedQuery(query: string) {
  Object.assign(urlState, parseUrlState(`?${query}`));
}

function onToggleCollapse(issueId: number) {
  const at = urlState.collapsed.indexOf(issueId);
  if (at === -1) urlState.collapsed.push(issueId);
  else urlState.collapsed.splice(at, 1);
}

onMounted(() => {
  if (!props.config.repoId) return;
  store.loadAll();
  store.startAutoRefresh();
});
onUnmounted(() => store.stopAutoRefresh());
</script>

<template>
  <div class="tw-flex tw-flex-col tw-gap-4">
    <ProjectPicker v-if="!config.repoId" :config="config"/>

    <template v-else>
      <div class="tw-flex tw-items-center tw-justify-between tw-flex-wrap tw-gap-2">
        <h2 class="ui header">{{ config.repoFullName }}</h2>
        <div class="ui secondary pointing menu">
          <a
            v-for="tab in tabs" :key="tab" class="item" :class="{active: urlState.view === tab}"
            @click="urlState.view = tab"
          >{{ tab }}</a>
        </div>
      </div>

      <div v-if="!config.token" class="ui negative message">
        <p>The page could not get a write token, so editing is turned off.</p>
        <p><strong>Suggested action:</strong> Reload the page.</p>
      </div>
      <div v-else-if="errorBanner" class="ui negative message">
        <p>{{ errorBanner.message }}</p>
        <p v-if="errorBanner.suggestedAction"><strong>Suggested action:</strong> {{ errorBanner.suggestedAction }}</p>
      </div>

      <p v-if="store.state.loadingBoard || store.state.loadingRoadmap" class="tw-text-text-light">Loading…</p>

      <div class="tw-flex tw-items-center tw-gap-2 tw-flex-wrap">
        <FilterBar v-model="urlState.q"/>
        <select v-model="urlState.group_by" class="ui dropdown">
          <option v-for="g in groupings" :key="g" :value="g">{{ g }}</option>
        </select>
        <SavedViews :store="store" :can-write="canWrite" :current-search="currentSearch" @apply="applySavedQuery"/>
      </div>

      <TableView
        v-if="urlState.view === 'table'" :store="store" :group-by="groupBy" :query="urlState.q"
        :collapsed="urlState.collapsed" @toggle-collapse="onToggleCollapse"
      />
      <BoardView
        v-else-if="urlState.view === 'board'" :store="store" :group-by="groupBy" :query="urlState.q"
        :can-edit-issues="canEditIssues"
      />
      <RoadmapView
        v-else-if="urlState.view === 'roadmap'" :store="store" :can-edit-issues="canEditIssues"
        :scale="urlState.scale" :at="urlState.at" :group-by="urlState.group_by" :collapsed="urlState.collapsed"
        @update:scale="urlState.scale = $event" @update:at="urlState.at = $event"
        @update:group-by="urlState.group_by = $event" @toggle-collapse="onToggleCollapse"
      />
      <TimeView
        v-else-if="urlState.view === 'time'" :store="store" :can-edit-issues="canEditIssues"
        :at="urlState.at" @update:at="urlState.at = $event"
      />
    </template>
  </div>
</template>
