<script lang="ts" setup>
import {onMounted, reactive, ref} from 'vue';
import {createField, deleteField, getFields, updateField} from '../../api.ts';
import type {PlanningApiConfig, PlanningScope} from '../../api.ts';
import type {Field} from '../../types.ts';

const props = defineProps<{config: PlanningApiConfig; scope: PlanningScope; canWrite: boolean}>();
const emit = defineEmits<{(e: 'error', err: unknown): void}>();

const kinds = ['int', 'text', 'date', 'select'];
const rows = ref<Field[]>([]);
const editingId = ref(0);
const edit = reactive({key: '', label: '', kind: '', options: '', required: false});
const draft = reactive({key: '', label: '', kind: 'text', options: '', required: false});

function ownScope(row: Field): boolean {
  if (props.scope.repoId) return row.scope === 'repo' && row.scope_id === props.scope.repoId;
  if (props.scope.orgId) return row.scope === 'org' && row.scope_id === props.scope.orgId;
  return row.scope === 'instance';
}

function toOptions(csv: string): string[] {
  return csv.split(',').map((s) => s.trim()).filter(Boolean);
}

async function load() {
  try {
    rows.value = await getFields(props.config, props.scope);
  } catch (err) {
    emit('error', err);
  }
}

async function onCreate() {
  try {
    await createField(props.config, {
      repo_id: props.scope.repoId, org_id: props.scope.orgId,
      key: draft.key, label: draft.label, kind: draft.kind, required: draft.required,
      options: draft.kind === 'select' ? toOptions(draft.options) : undefined,
    });
    draft.key = '';
    draft.label = '';
    draft.options = '';
    await load();
  } catch (err) {
    emit('error', err);
  }
}

function startEdit(row: Field) {
  editingId.value = row.id;
  Object.assign(edit, {key: row.key, label: row.label, kind: row.kind, options: (row.options ?? []).join(', '), required: row.required});
}

async function onSaveEdit(row: Field) {
  try {
    await updateField(props.config, row.id, {
      repo_id: props.scope.repoId, org_id: props.scope.orgId,
      key: edit.key, label: edit.label, kind: edit.kind, required: edit.required,
      options: edit.kind === 'select' ? toOptions(edit.options) : undefined,
    });
    editingId.value = 0;
    await load();
  } catch (err) {
    emit('error', err);
  }
}

async function onDelete(row: Field) {
  try {
    await deleteField(props.config, row.id);
    await load();
  } catch (err) {
    emit('error', err);
  }
}

onMounted(load);
</script>

<template>
  <div class="tw-flex tw-flex-col tw-gap-3">
    <table class="ui very basic table">
      <thead>
        <tr><th>Key</th><th>Label</th><th>Kind</th><th>Options</th><th>Required</th><th>Scope</th><th v-if="canWrite"/></tr>
      </thead>
      <tbody>
        <tr v-for="row in rows" :key="row.id">
          <template v-if="editingId === row.id">
            <td>{{ row.key }}</td>
            <td><input v-model="edit.label" class="tw-w-full"></td>
            <td>{{ row.kind }}</td>
            <td><input v-if="edit.kind === 'select'" v-model="edit.options" placeholder="a, b, c" class="tw-w-full"></td>
            <td><input v-model="edit.required" type="checkbox"></td>
            <td>{{ row.scope }}</td>
            <td>
              <button type="button" class="ui mini primary button" @click="onSaveEdit(row)">Save</button>
              <button type="button" class="ui mini button" @click="editingId = 0">Cancel</button>
            </td>
          </template>
          <template v-else>
            <td>{{ row.key }}</td>
            <td>{{ row.label }}</td>
            <td>{{ row.kind }}</td>
            <td>{{ (row.options ?? []).join(', ') }}</td>
            <td>{{ row.required ? 'yes' : 'no' }}</td>
            <td>{{ row.scope }}</td>
            <td v-if="canWrite">
              <template v-if="ownScope(row)">
                <button type="button" class="ui mini button" @click="startEdit(row)">Edit</button>
                <button type="button" class="ui mini negative button" @click="onDelete(row)">Delete</button>
              </template>
            </td>
          </template>
        </tr>
      </tbody>
    </table>

    <form v-if="canWrite" class="ui form tw-flex tw-items-end tw-gap-2 tw-flex-wrap" @submit.prevent="onCreate">
      <div class="field"><label>Key<input v-model="draft.key" required></label></div>
      <div class="field"><label>Label<input v-model="draft.label" required></label></div>
      <div class="field">
        <label>Kind
          <select v-model="draft.kind" class="ui dropdown">
            <option v-for="kind in kinds" :key="kind" :value="kind">{{ kind }}</option>
          </select>
        </label>
      </div>
      <div v-if="draft.kind === 'select'" class="field"><label>Options<input v-model="draft.options" placeholder="a, b, c"></label></div>
      <div class="field tw-flex tw-items-center tw-gap-1"><label>Required<input v-model="draft.required" type="checkbox"></label></div>
      <button type="submit" class="ui primary button">Create field</button>
    </form>
  </div>
</template>
