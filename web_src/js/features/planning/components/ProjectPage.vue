<script lang="ts" setup>
import {onMounted, ref} from 'vue';
import {call, paths, ApiError} from '../api.ts';
import type {PlanningProjectConfig} from '../project.ts';

type BoardGroup = {cards: number};
type Board = {groups: BoardGroup[]};

const props = defineProps<{config: PlanningProjectConfig}>();

const cardCount = ref<number | null>(null);
const errorMessage = ref('');
const errorAction = ref('');

onMounted(async () => {
  if (!props.config.repoId) return;
  try {
    const params = new URLSearchParams({
      repo_id: String(props.config.repoId),
      project_id: String(props.config.projectId),
    });
    const board = await call<Board>(props.config, `${paths.board}${params}`);
    cardCount.value = board.groups.reduce((sum, group) => sum + group.cards, 0);
  } catch (err) {
    if (err instanceof ApiError) {
      errorMessage.value = err.message;
      errorAction.value = err.suggestedAction;
    } else {
      errorMessage.value = String(err);
    }
  }
});
</script>

<template>
  <div class="tw-flex tw-flex-col tw-gap-4">
    <h2 class="ui header">
      Projects
      <template v-if="config.repoFullName">— {{ config.repoFullName }}</template>
      <template v-if="config.projectId">#{{ config.projectId }}</template>
    </h2>

    <div v-if="errorMessage" class="ui negative message">
      <p>{{ errorMessage }}</p>
      <p v-if="errorAction"><strong>Suggested action:</strong> {{ errorAction }}</p>
    </div>

    <p v-else-if="!config.repoId">Choose a project</p>
    <p v-else-if="cardCount !== null">{{ cardCount }} cards</p>
  </div>
</template>
