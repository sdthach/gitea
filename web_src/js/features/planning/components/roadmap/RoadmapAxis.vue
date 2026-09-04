<script lang="ts" setup>
import {computed} from 'vue';
import {PX_PER_DAY, weekendDayIndexes} from '../../scale.ts';
import type {Scale, Tick} from '../../scale.ts';

const props = defineProps<{ticks: Tick[]; widthPx: number; todayX: number | null; origin: number; days: number; scale: Scale}>();

// Weekend shading only reads at day and week scale: at month or quarter scale a single day's
// column is too thin to shade meaningfully.
const weekendBands = computed(() => (props.scale === 'day' || props.scale === 'week')
  ? weekendDayIndexes(props.origin, props.days).map((i) => ({x: i * PX_PER_DAY[props.scale], width: PX_PER_DAY[props.scale]}))
  : []);
</script>

<template>
  <div class="planning-roadmap-axis tw-sticky tw-top-0 tw-z-10 tw-relative tw-bg-body tw-border-b tw-h-8" :style="{width: `${widthPx}px`}">
    <span
      v-for="band in weekendBands" :key="`weekend-${band.x}`" class="tw-absolute tw-inset-y-0 tw-bg-hover tw-pointer-events-none"
      :style="{left: `${band.x}px`, width: `${band.width}px`}"
    />
    <span
      v-for="tick in ticks" :key="tick.unix" class="tw-absolute tw-top-2 tw-text-text-light tw-text-xs tw-whitespace-nowrap"
      :style="{left: `${tick.x}px`}"
    >{{ tick.label }}</span>
    <span v-if="todayX !== null" class="tw-absolute tw-inset-y-0 tw-w-0.5 tw-bg-red" :style="{left: `${todayX}px`}"/>
  </div>
</template>
