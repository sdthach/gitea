<script lang="ts" setup>
import {onMounted, reactive, ref} from 'vue';
import {ApiError, createIssueType, deleteIssueType, getIssueTypes, updateIssueType} from '../../api.ts';
import type {PlanningApiConfig, PlanningScope} from '../../api.ts';
import type {IssueType} from '../../types.ts';

const props = defineProps<{config: PlanningApiConfig; scope: PlanningScope; canWrite: boolean}>();
const emit = defineEmits<{(e: 'error', err: unknown): void}>();

const rows = ref<IssueType[]>([]);
const editingId = ref(0);
const confirmDeleteId = ref(0);
const edit = reactive({name: '', color: '', icon: '', rank: 5});
const draft = reactive({name: '', color: '#6b7280', icon: 'octicon-issue-opened', rank: 5});

function ownScope(row: IssueType): boolean {
  if (props.scope.repoId) return row.scope === 'repo' && row.scope_id === props.scope.repoId;
  if (props.scope.orgId) return row.scope === 'org' && row.scope_id === props.scope.orgId;
  return row.scope === 'instance';
}

async function load() {
  try {
    rows.value = await getIssueTypes(props.config, props.scope);
  } catch (err) {
    emit('error', err);
  }
}

async function onCreate() {
  try {
    await createIssueType(props.config, {repo_id: props.scope.repoId, org_id: props.scope.orgId, ...draft});
    draft.name = '';
    await load();
  } catch (err) {
    emit('error', err);
  }
}

function startEdit(row: IssueType) {
  editingId.value = row.id;
  Object.assign(edit, {name: row.name, color: row.color, icon: row.icon, rank: row.rank});
}

async function onSaveEdit(row: IssueType) {
  try {
    await updateIssueType(props.config, row.id, {repo_id: props.scope.repoId, org_id: props.scope.orgId, ...edit});
    editingId.value = 0;
    await load();
  } catch (err) {
    emit('error', err);
  }
}

async function onDelete(row: IssueType, force = false) {
  try {
    await deleteIssueType(props.config, row.id, force);
    confirmDeleteId.value = 0;
    await load();
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      confirmDeleteId.value = row.id;
      return;
    }
    emit('error', err);
  }
}

onMounted(load);
</script>

<template>
  <div class="tw-flex tw-flex-col tw-gap-3">
    <table class="ui very basic table">
      <thead>
        <tr><th>Name</th><th>Color</th><th>Icon</th><th>Rank</th><th>Scope</th><th v-if="canWrite"/></tr>
      </thead>
      <tbody>
        <tr v-for="row in rows" :key="row.id">
          <template v-if="editingId === row.id">
            <td><input v-model="edit.name" class="tw-w-full"></td>
            <td><input v-model="edit.color" class="tw-w-full"></td>
            <td><input v-model="edit.icon" class="tw-w-full"></td>
            <td><input v-model.number="edit.rank" type="number" min="1" max="9" class="tw-w-16"></td>
            <td>{{ row.scope }}</td>
            <td>
              <button type="button" class="ui mini primary button" @click="onSaveEdit(row)">Save</button>
              <button type="button" class="ui mini button" @click="editingId = 0">Cancel</button>
            </td>
          </template>
          <template v-else>
            <td>{{ row.name }}</td>
            <td><span class="tw-inline-block tw-w-4 tw-h-4 tw-rounded-full" :style="{background: row.color}"/> {{ row.color }}</td>
            <td>{{ row.icon }}</td>
            <td>{{ row.rank }}</td>
            <td>{{ row.scope }}</td>
            <td v-if="canWrite">
              <template v-if="ownScope(row)">
                <button type="button" class="ui mini button" @click="startEdit(row)">Edit</button>
                <button v-if="confirmDeleteId !== row.id" type="button" class="ui mini negative button" @click="onDelete(row)">Delete</button>
                <button v-else type="button" class="ui mini negative button" title="In use; delete anyway and clear its assignments" @click="onDelete(row, true)">Really delete?</button>
              </template>
            </td>
          </template>
        </tr>
      </tbody>
    </table>

    <form v-if="canWrite" class="ui form tw-flex tw-items-end tw-gap-2 tw-flex-wrap" @submit.prevent="onCreate">
      <div class="field"><label>Name<input v-model="draft.name" required></label></div>
      <div class="field"><label>Color<input v-model="draft.color" required></label></div>
      <div class="field"><label>Icon<input v-model="draft.icon" placeholder="octicon-bug" required></label></div>
      <div class="field"><label>Rank<input v-model.number="draft.rank" type="number" min="1" max="9" class="tw-w-16"></label></div>
      <button type="submit" class="ui primary button">Create type</button>
    </form>
  </div>
</template>
