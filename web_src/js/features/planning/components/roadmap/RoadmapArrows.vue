<script lang="ts" setup>
import {computed} from 'vue';
import {arrowPaths, geometryKeyFor} from '../../arrows.ts';
import type {ArrowRect} from '../../arrows.ts';
import type {Arrow} from '../../types.ts';

const props = defineProps<{
  arrows: Arrow[];
  geometry: Map<string, ArrowRect>;
  widthPx: number;
  heightPx: number;
  canEditIssues: boolean;
}>();

const emit = defineEmits<{(e: 'remove', arrow: Arrow): void}>();

// arrowByKey resolves a clicked path back to the arrow it was drawn for, keyed the same way
// arrowPaths itself keys a resolved path — so a lookup never drifts from what actually rendered.
const arrowByKey = computed(() => new Map(props.arrows.map((arrow) => [
  `${geometryKeyFor(arrow.from_issue_id, arrow.from_rollup)}>${geometryKeyFor(arrow.to_issue_id, arrow.to_rollup)}`, arrow,
])));

const paths = computed(() => arrowPaths(props.arrows, props.geometry));

function onArrowClick(key: string) {
  if (!props.canEditIssues) return;
  const arrow = arrowByKey.value.get(key);
  if (arrow) emit('remove', arrow);
}
</script>

<template>
  <svg
    class="planning-roadmap-arrows tw-absolute tw-inset-0 tw-pointer-events-none tw-overflow-visible"
    :width="widthPx" :height="heightPx"
  >
    <path
      v-for="arrow in paths" :key="arrow.key" :data-arrow="arrow.key" :d="arrow.path"
      fill="none" stroke="var(--color-text-light)" stroke-width="1.5"
      :stroke-dasharray="arrow.enforced ? undefined : '4 3'"
      :class="{'tw-cursor-pointer tw-pointer-events-auto': canEditIssues}"
      @click="onArrowClick(arrow.key)"
    />
  </svg>
</template>
