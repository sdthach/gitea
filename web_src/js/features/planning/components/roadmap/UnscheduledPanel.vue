<script lang="ts" setup>
import type {Unmanaged} from '../../types.ts';

export type UnscheduledBar = {issue_id: number; number: number; title: string; url: string};

defineProps<{unmanaged: Unmanaged[]; unscheduledBars: UnscheduledBar[]; canEditIssues: boolean}>();

function onDragStart(issueId: number, event: DragEvent) {
  event.dataTransfer!.setData('text/plain', String(issueId));
  event.dataTransfer!.effectAllowed = 'move';
}
</script>

<template>
  <aside class="planning-unscheduled tw-w-64 tw-flex-shrink-0 tw-flex tw-flex-col tw-gap-2 tw-overflow-y-auto">
    <div v-if="unscheduledBars.length">
      <h4 class="tw-font-semibold tw-text-sm">Needs a start</h4>
      <div
        v-for="bar in unscheduledBars" :key="bar.issue_id" :draggable="canEditIssues" :data-drag="canEditIssues || undefined"
        class="tw-p-1 tw-border tw-rounded tw-text-sm tw-cursor-grab"
        @dragstart="onDragStart(bar.issue_id, $event)"
      >
        <a :href="bar.url">{{ bar.title }} <span class="tw-text-text-light">#{{ bar.number }}</span></a>
      </div>
    </div>
    <div v-if="unmanaged.length">
      <h4 class="tw-font-semibold tw-text-sm">Unmanaged</h4>
      <div
        v-for="item in unmanaged" :key="item.issue_id" :draggable="canEditIssues" :data-drag="canEditIssues || undefined"
        class="tw-p-1 tw-border tw-rounded tw-text-sm tw-cursor-grab"
        @dragstart="onDragStart(item.issue_id, $event)"
      >
        <a :href="item.url">{{ item.title }} <span class="tw-text-text-light">#{{ item.number }}</span></a>
        <div class="tw-text-text-light tw-text-xs">{{ item.reason }} — {{ item.suggested_action }}</div>
      </div>
    </div>
  </aside>
</template>
