<script lang="ts" setup>
import {computed, reactive} from 'vue';
import {PX_PER_DAY, unixAtX, xOf} from '../../scale.ts';
import type {Scale} from '../../scale.ts';
import {isoDate} from '../../drag.ts';
import type {CapacitySprint, RoadmapMilestone} from '../../types.ts';

const props = defineProps<{
  milestones: RoadmapMilestone[];
  sprints: CapacitySprint[];
  origin: number;
  scale: Scale;
  heightPx: number;
  canEditIssues: boolean;
}>();

const emit = defineEmits<{(e: 'schedule', payload: {milestoneId: number; start: string}): void}>();

// preview holds a milestone's own start while its left edge is being dragged, so the band and
// its due marker move live before the write settles.
const preview = reactive<Record<number, number>>({});

function startOf(milestone: RoadmapMilestone): number {
  return preview[milestone.milestone_id] ?? milestone.start_unix;
}

const bands = computed(() => props.milestones
  .filter((m) => m.start_unix > 0 && m.end_unix > 0)
  .map((m) => {
    const left = xOf(startOf(m), props.origin, props.scale);
    const sprint = props.sprints.find((s) => s.milestone_id === m.milestone_id);
    const load = sprint ? sprint.lanes.reduce((sum, lane) => sum + lane.load_hours, 0) : 0;
    const available = sprint ? sprint.lanes.reduce((sum, lane) => sum + lane.available_hours, 0) : 0;
    return {
      milestone: m,
      left,
      width: Math.max(1, xOf(m.end_unix, props.origin, props.scale) - left),
      summary: sprint ? `${Math.round(load)}h / ${Math.round(available)}h` : '',
    };
  }));

// dueMarkers place a mark for every milestone with a due date, scheduled or not: an unscheduled
// milestone still owes the roadmap a deadline marker even with no band to draw it on.
const dueMarkers = computed(() => props.milestones
  .filter((m) => m.end_unix > 0)
  .map((m) => ({milestone: m, x: xOf(m.end_unix, props.origin, props.scale)})));

let dragMilestoneId: number | null = null;
let pointerId: number | null = null;
let startX = 0;

function onEdgePointerDown(milestone: RoadmapMilestone, event: PointerEvent) {
  if (!props.canEditIssues) return;
  event.preventDefault();
  (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId);
  dragMilestoneId = milestone.milestone_id;
  pointerId = event.pointerId;
  startX = event.clientX;
  preview[milestone.milestone_id] = milestone.start_unix;
}

function onEdgePointerMove(milestone: RoadmapMilestone, event: PointerEvent) {
  if (dragMilestoneId !== milestone.milestone_id || event.pointerId !== pointerId) return;
  const dxDays = Math.round((event.clientX - startX) / PX_PER_DAY[props.scale]);
  preview[milestone.milestone_id] = unixAtX(xOf(milestone.start_unix, props.origin, props.scale) + dxDays * PX_PER_DAY[props.scale], props.origin, props.scale);
}

function onEdgePointerUp(milestone: RoadmapMilestone, event: PointerEvent) {
  if (dragMilestoneId !== milestone.milestone_id || event.pointerId !== pointerId) return;
  (event.currentTarget as HTMLElement).releasePointerCapture!(event.pointerId);
  const proposed = preview[milestone.milestone_id];
  dragMilestoneId = null;
  pointerId = null;
  delete preview[milestone.milestone_id];
  if (proposed !== undefined && proposed !== milestone.start_unix) {
    emit('schedule', {milestoneId: milestone.milestone_id, start: isoDate(proposed)});
  }
}

function onEdgePointerCancel(milestone: RoadmapMilestone) {
  dragMilestoneId = null;
  pointerId = null;
  delete preview[milestone.milestone_id];
}
</script>

<template>
  <div class="planning-sprint-bands tw-absolute tw-inset-0 tw-pointer-events-none" :style="{height: `${heightPx}px`}">
    <div
      v-for="band in bands" :key="band.milestone.milestone_id" class="planning-sprint-band tw-absolute tw-inset-y-0 tw-bg-secondary-bg"
      :style="{left: `${band.left}px`, width: `${band.width}px`}"
    >
      <span class="tw-sticky tw-top-8 tw-text-text-light tw-text-xs tw-pl-1 tw-pointer-events-none">{{ band.milestone.title }} {{ band.summary }}</span>
      <span
        v-if="canEditIssues" data-drag class="planning-sprint-band-handle tw-absolute tw-inset-y-0 tw-left-0 tw-w-1 tw-cursor-ew-resize tw-pointer-events-auto"
        @pointerdown="onEdgePointerDown(band.milestone, $event)"
        @pointermove="onEdgePointerMove(band.milestone, $event)"
        @pointerup="onEdgePointerUp(band.milestone, $event)"
        @pointercancel="onEdgePointerCancel(band.milestone)"
      />
    </div>
    <span
      v-for="marker in dueMarkers" :key="`due-${marker.milestone.milestone_id}`" class="planning-sprint-due tw-absolute tw-inset-y-0 tw-w-px tw-bg-text-light"
      :style="{left: `${marker.x}px`}" :title="`${marker.milestone.title} due`"
    />
  </div>
</template>
