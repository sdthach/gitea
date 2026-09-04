<script lang="ts" setup>
import {ref, watch} from 'vue';

const DEBOUNCE_MS = 150;

const props = defineProps<{modelValue: string}>();
const emit = defineEmits<{(e: 'update:modelValue', value: string): void}>();

const text = ref(props.modelValue);
let timer: ReturnType<typeof setTimeout> | null = null;

watch(() => props.modelValue, (value) => {
  if (value !== text.value) text.value = value;
});

function onInput() {
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => emit('update:modelValue', text.value), DEBOUNCE_MS);
}
</script>

<template>
  <input
    v-model="text"
    type="search"
    class="ui input tw-w-full"
    placeholder="Filter, e.g. is:open type:bug assignee:me"
    @input="onInput"
  >
</template>
