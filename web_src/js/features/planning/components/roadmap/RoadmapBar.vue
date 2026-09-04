<script lang="ts" setup>
import {computed, onBeforeUnmount, reactive, watch} from 'vue';
import SvgIcon from '../../../../components/SvgIcon.vue';
import type {SvgName} from '../../../../svg.ts';
import {PX_PER_DAY, xOf} from '../../scale.ts';
import type {Scale} from '../../scale.ts';
import {begin, isoDate, targetRowIndex} from '../../drag.ts';
import type {Drag, DragContext, DragWrite, RowGeometry} from '../../drag.ts';
import type {RoadmapBarModel} from '../../types.ts';

const props = defineProps<{
  bar: RoadmapBarModel;
  origin: number;
  scale: Scale;
  rowGeometry: RowGeometry;
  rowIndex: number;
  canEditIssues: boolean;
  top: number;
  height: number;
}>();

const emit = defineEmits<{
  (e: 'commit', writes: DragWrite[]): void;
  (e: 'link', payload: {fromIssueId: number; toIssueId: number}): void;
}>();

// A dependency drag carries its own mime type, distinct from the plain issue id an unscheduled
// card's drag sets: onRowDrop (RoadmapView) reads only text/plain, so a dependency drop bubbling
// past this bar never gets mistaken for a schedule drop.
const DEPENDENCY_MIME = 'application/x-planning-dependency';

function onArrowHandleDragStart(event: DragEvent) {
  if (!props.canEditIssues) return;
  event.dataTransfer!.setData(DEPENDENCY_MIME, String(props.bar.issueId));
  event.dataTransfer!.effectAllowed = 'link';
}

function onArrowDrop(event: DragEvent) {
  if (!event.dataTransfer?.types.includes(DEPENDENCY_MIME)) return;
  event.stopPropagation();
  const fromIssueId = Number(event.dataTransfer.getData(DEPENDENCY_MIME));
  if (fromIssueId && fromIssueId !== props.bar.issueId) emit('link', {fromIssueId, toIssueId: props.bar.issueId});
}

const preview = reactive({startUnix: props.bar.startUnix, endUnix: props.bar.endUnix});

let drag: Drag | null = null;
let pointerId: number | null = null;

function ctx(): DragContext {
  return {origin: props.origin, scale: props.scale, rowGeometry: props.rowGeometry};
}

function dragBar() {
  return {issueId: props.bar.issueId, startUnix: props.bar.startUnix, endUnix: props.bar.endUnix, rowKey: props.bar.rowKey};
}

function resetPreview() {
  preview.startUnix = props.bar.startUnix;
  preview.endUnix = props.bar.endUnix;
}

// A committed drag's own writes land back on props.bar (through the store, once the server
// round-trip settles) rather than snapping the preview back to the pre-drag position first.
watch(() => [props.bar.startUnix, props.bar.endUnix], resetPreview);

// onWindowEscape cancels whichever drag is live, the same path pointercancel takes, so pressing
// Escape mid-drag leaves the bar exactly where it started and writes nothing.
function onWindowEscape(event: KeyboardEvent) {
  if (event.key !== 'Escape' || !drag) return;
  drag.cancel();
  drag = null;
  pointerId = null;
  resetPreview();
  window.removeEventListener('keydown', onWindowEscape);
}

function startDrag(kind: 'move' | 'resize-start' | 'resize-end', event: PointerEvent) {
  if (!props.canEditIssues || event.button !== 0) return;
  event.preventDefault();
  (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId);
  pointerId = event.pointerId;
  drag = begin(kind, dragBar(), event.clientX, event.clientY, ctx());
  window.addEventListener('keydown', onWindowEscape);
}

function onPointerMove(event: PointerEvent) {
  if (!drag || event.pointerId !== pointerId) return;
  const proposal = drag.update(event.clientX, event.clientY);
  preview.startUnix = proposal.start;
  preview.endUnix = proposal.end;
}

function finishDrag(event: PointerEvent, commitIt: boolean) {
  if (!drag || event.pointerId !== pointerId) return;
  (event.currentTarget as HTMLElement).releasePointerCapture!(event.pointerId);
  window.removeEventListener('keydown', onWindowEscape);
  let writes: DragWrite[] = [];
  if (commitIt) writes = drag.commit();
  else drag.cancel();
  drag = null;
  pointerId = null;
  if (writes.length) emit('commit', writes);
  resetPreview();
}

function onPointerUp(event: PointerEvent) {
  finishDrag(event, true);
}

function onPointerCancel(event: PointerEvent) {
  finishDrag(event, false);
}

onBeforeUnmount(() => window.removeEventListener('keydown', onWindowEscape));

function runKeyboardDrag(kind: 'move' | 'resize-start' | 'resize-end' | 'row', x: number, y: number) {
  const kbDrag = begin(kind, dragBar(), 0, 0, ctx());
  kbDrag.update(x, y);
  const writes = kbDrag.commit();
  if (writes.length) emit('commit', writes);
}

// Alt+Left/Right moves both dates by a day; Alt+Shift+Left grows the start edge a day earlier,
// Alt+Shift+Right grows the end edge a day later; Alt+Up/Down hands the bar to the row above or
// below in the current row geometry.
function onKeydown(event: KeyboardEvent) {
  if (!props.canEditIssues || !event.altKey) return;
  if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
    event.preventDefault();
    const rows = props.rowGeometry.rows;
    const targetIndex = targetRowIndex(props.rowIndex, event.key === 'ArrowUp' ? 'up' : 'down', rows.length);
    if (targetIndex === null) return;
    const target = rows[targetIndex];
    runKeyboardDrag('row', 0, target.top + props.rowGeometry.rowHeight / 2);
    return;
  }
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
  event.preventDefault();
  const days = event.key === 'ArrowLeft' ? -1 : 1;
  const kind = !event.shiftKey ? 'move' : (event.key === 'ArrowLeft' ? 'resize-start' : 'resize-end');
  runKeyboardDrag(kind, days * PX_PER_DAY[props.scale], 0);
}

const left = computed(() => xOf(preview.startUnix, props.origin, props.scale));
const width = computed(() => Math.max(1, xOf(preview.endUnix, props.origin, props.scale) - left.value));
</script>

<template>
  <div
    class="planning-roadmap-bar tw-absolute tw-flex tw-items-center tw-gap-1 tw-rounded tw-px-1 tw-text-xs tw-text-white tw-overflow-hidden"
    :class="{'tw-border tw-border-dashed tw-opacity-80': bar.endInferred}"
    :style="{left: `${left}px`, width: `${width}px`, top: `${top}px`, height: `${height}px`, backgroundColor: bar.typeColor || 'var(--color-primary)'}"
    :data-start="isoDate(preview.startUnix)"
    :data-end="isoDate(preview.endUnix)"
    :tabindex="canEditIssues ? 0 : -1"
    @keydown="onKeydown"
    @dragover.prevent
    @drop="onArrowDrop"
  >
    <span
      v-if="canEditIssues" data-drag class="tw-w-1.5 tw-flex-shrink-0 tw-cursor-ew-resize"
      @pointerdown="startDrag('resize-start', $event)" @pointermove="onPointerMove" @pointerup="onPointerUp" @pointercancel="onPointerCancel"
    />
    <span
      v-if="canEditIssues" data-drag class="planning-roadmap-bar-body tw-flex-1 tw-flex tw-items-center tw-gap-1 tw-overflow-hidden tw-cursor-grab"
      @pointerdown="startDrag('move', $event)" @pointermove="onPointerMove" @pointerup="onPointerUp" @pointercancel="onPointerCancel"
    >
      <svg-icon v-if="bar.typeIcon" :name="(bar.typeIcon as SvgName)"/>
      <span class="tw-truncate">{{ bar.title }} <span class="tw-opacity-75">#{{ bar.number }}</span></span>
    </span>
    <span v-else class="tw-flex-1 tw-flex tw-items-center tw-gap-1 tw-overflow-hidden">
      <svg-icon v-if="bar.typeIcon" :name="(bar.typeIcon as SvgName)"/>
      <span class="tw-truncate">{{ bar.title }} <span class="tw-opacity-75">#{{ bar.number }}</span></span>
    </span>
    <span
      v-if="canEditIssues" data-drag class="tw-w-1.5 tw-flex-shrink-0 tw-cursor-ew-resize"
      @pointerdown="startDrag('resize-end', $event)" @pointermove="onPointerMove" @pointerup="onPointerUp" @pointercancel="onPointerCancel"
    />
    <span
      v-if="canEditIssues" draggable="true" data-arrow-handle
      class="tw-absolute tw-right-0 tw-top-0 tw-bottom-0 tw-my-auto tw-w-2 tw-h-2 tw-rounded-full tw-bg-white tw-cursor-crosshair"
      title="Drag onto another bar to add a dependency"
      @dragstart="onArrowHandleDragStart"
    />
  </div>
</template>
