<script lang="ts" setup>
import {onMounted, ref} from 'vue';
import {getProjects, ApiError} from '../api.ts';
import type {PlanningApiConfig} from '../api.ts';
import type {ProjectsPickerProject, ProjectsPickerRepo} from '../types.ts';

const props = defineProps<{config: PlanningApiConfig}>();

const repos = ref<ProjectsPickerRepo[]>([]);
const projects = ref<ProjectsPickerProject[]>([]);
const repoId = ref(0);
const errorMessage = ref('');

async function loadRepos() {
  try {
    const page = await getProjects(props.config);
    repos.value = page.repos;
    errorMessage.value = '';
  } catch (err) {
    errorMessage.value = err instanceof ApiError ? err.message : String(err);
  }
}

async function onRepoChange() {
  projects.value = [];
  if (!repoId.value) return;
  try {
    const page = await getProjects(props.config, repoId.value);
    projects.value = page.projects;
    errorMessage.value = '';
  } catch (err) {
    errorMessage.value = err instanceof ApiError ? err.message : String(err);
  }
}

function onProjectChange(event: Event) {
  const projectId = Number((event.target as HTMLSelectElement).value);
  if (!projectId) return;
  const repo = repos.value.find((r) => r.id === repoId.value);
  if (!repo) return;
  const prefix = window.location.pathname.replace(/\/planning\/projects\/?$/, '');
  window.location.href = `${prefix}/planning/projects/${repo.owner}/${repo.name}/${projectId}`;
}

onMounted(loadRepos);
</script>

<template>
  <div class="tw-flex tw-flex-col tw-gap-4">
    <h2 class="ui header">Projects</h2>
    <div v-if="errorMessage" class="ui negative message">{{ errorMessage }}</div>
    <div class="tw-flex tw-gap-2">
      <select v-model.number="repoId" class="ui dropdown" @change="onRepoChange">
        <option :value="0" disabled>Choose a repository</option>
        <option v-for="repo in repos" :key="repo.id" :value="repo.id" :disabled="!repo.projects_enabled">
          {{ repo.full_name }}
        </option>
      </select>
      <select v-if="projects.length" class="ui dropdown" @change="onProjectChange">
        <option :value="0" disabled selected>Choose a project</option>
        <option v-for="project in projects" :key="project.id" :value="project.id">
          {{ project.title }}
        </option>
      </select>
    </div>
  </div>
</template>
