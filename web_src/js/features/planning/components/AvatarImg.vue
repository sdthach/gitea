<script lang="ts" setup>
import {computed, ref} from 'vue';

// avatarUrl, when the caller has it (the API's own resolved assignee_avatars), is used as is —
// it already honours gravatar and local avatar settings. Without it this falls back to the
// same constructed link every other caller of this component used before that field existed.
const props = defineProps<{login: string; size: number; avatarUrl?: string}>();
const failed = ref(false);

const src = computed(() => props.avatarUrl || `/user/avatar/${props.login}/${props.size}`);

function initials(login: string): string {
  return login.slice(0, 2).toUpperCase();
}
</script>

<template>
  <img
    v-if="!failed" class="ui avatar image" :src="src"
    :width="size" :height="size" :alt="login" @error="failed = true"
  >
  <span
    v-else
    class="tw-inline-flex tw-items-center tw-justify-center tw-rounded-full tw-bg-secondary-bg tw-text-text-light"
    :style="{width: `${size}px`, height: `${size}px`, fontSize: `${size * 0.4}px`}"
    :title="login"
  >{{ initials(login) }}</span>
</template>
