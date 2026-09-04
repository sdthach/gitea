<script lang="ts" setup>
import {onMounted} from 'vue';
import type {PlanningStore} from '../store.ts';

const props = defineProps<{store: PlanningStore; canWrite: boolean; currentSearch: string}>();
const emit = defineEmits<{(e: 'apply', query: string): void}>();

onMounted(() => props.store.loadViews());

async function onSave() {
  const name = window.prompt('Name this view');
  if (!name) return;
  await props.store.saveView(name, props.currentSearch);
}

async function onDelete(id: number) {
  await props.store.removeView(id);
}
</script>

<template>
  <div class="tw-flex tw-flex-wrap tw-items-center tw-gap-2">
    <span
      v-for="view in store.state.views"
      :key="view.id"
      class="ui label tw-flex tw-items-center tw-gap-1"
    >
      <a href="javascript:void(0)" @click="emit('apply', view.query)">{{ view.name }}</a>
      <a v-if="canWrite" href="javascript:void(0)" title="Delete view" @click="onDelete(view.id)">×</a>
    </span>
    <button v-if="canWrite" type="button" class="ui tiny button" @click="onSave">Save view</button>
  </div>
</template>
