<script lang="ts" setup>
import {computed} from 'vue';
import {PX_PER_DAY, xOf} from '../../scale.ts';
import type {Scale} from '../../scale.ts';
import type {CapacityLane} from '../../types.ts';

const props = defineProps<{lane: CapacityLane | undefined; origin: number; scale: Scale}>();

// heat is how loud a day's shading reads: 0 with no recorded load, up to 1 at full available
// hours, capped there — over days get their own red rather than a louder version of this scale.
function heat(loadHours: number, availableHours: number): number {
  if (availableHours <= 0) return loadHours > 0 ? 1 : 0;
  return Math.min(1, loadHours / availableHours);
}

const cells = computed(() => (props.lane?.days ?? []).map((day) => ({
  unix: day.unix,
  left: xOf(day.unix, props.origin, props.scale),
  over: day.over,
  heat: heat(day.load_hours, day.available_hours),
})));

const cellWidth = computed(() => PX_PER_DAY[props.scale]);
</script>

<template>
  <div v-if="lane" class="planning-capacity-strip tw-relative tw-h-1.5">
    <span
      v-for="cell in cells" :key="cell.unix" class="tw-absolute tw-inset-y-0"
      :class="cell.over ? 'tw-bg-red' : 'tw-bg-orange'"
      :style="{left: `${cell.left}px`, width: `${cellWidth}px`, opacity: cell.over ? 1 : cell.heat}"
    />
  </div>
</template>
