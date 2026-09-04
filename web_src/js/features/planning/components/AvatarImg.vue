<script lang="ts" setup>
import {ref} from 'vue';

const props = defineProps<{login: string; size: number}>();
const failed = ref(false);

function initials(login: string): string {
  return login.slice(0, 2).toUpperCase();
}
</script>

<template>
  <img
    v-if="!failed" class="ui avatar image" :src="`/user/avatar/${login}/${size}`"
    :width="size" :height="size" :alt="login" @error="failed = true"
  >
  <span
    v-else
    class="tw-inline-flex tw-items-center tw-justify-center tw-rounded-full tw-bg-secondary-bg tw-text-text-light"
    :style="{width: `${size}px`, height: `${size}px`, fontSize: `${size * 0.4}px`}"
    :title="login"
  >{{ initials(login) }}</span>
</template>
