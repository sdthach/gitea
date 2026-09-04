<script lang="ts" setup>
import {computed} from 'vue';
import {xOf} from '../../scale.ts';
import type {Scale} from '../../scale.ts';
import type {RollupRow} from '../../types.ts';

const props = defineProps<{rollup: RollupRow; origin: number; scale: Scale; top: number; height: number}>();

const left = computed(() => xOf(props.rollup.start_unix, props.origin, props.scale));
const width = computed(() => Math.max(1, xOf(props.rollup.end_unix, props.origin, props.scale) - left.value));
const title = computed(() => props.rollup.warning || `${props.rollup.label}: ${props.rollup.closed}/${props.rollup.children} closed`);
</script>

<template>
  <div
    class="planning-bracket tw-absolute tw-border-t-2 tw-border-b-2 tw-border-text-light"
    :class="{'tw-border-red': rollup.warning}"
    :style="{left: `${left}px`, width: `${width}px`, top: `${top}px`, height: `${height}px`}"
    :title="title"
  />
</template>
