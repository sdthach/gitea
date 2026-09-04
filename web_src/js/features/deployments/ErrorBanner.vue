<script lang="ts" setup>
defineProps<{id?: string; header: string; message: string; suggestedAction?: string}>();
</script>

<template>
  <div v-if="message" :id="id || 'deployments-error'" class="ui negative message">
    <div class="header">{{ header }}</div>
    <p>{{ message }}</p>
    <p v-if="suggestedAction"><strong>Suggested action:</strong> {{ suggestedAction }}</p>
  </div>
</template>
