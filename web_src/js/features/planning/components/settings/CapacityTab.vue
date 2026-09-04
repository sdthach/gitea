<script lang="ts" setup>
import {onMounted, reactive, ref} from 'vue';
import {clearCapacityUser, getCapacity, getUserIDByLogin, setCapacityUser} from '../../api.ts';
import type {PlanningApiConfig, PlanningScope} from '../../api.ts';
import type {CapacityRow} from '../../types.ts';

const props = defineProps<{config: PlanningApiConfig; scope: PlanningScope; canWrite: boolean}>();
const emit = defineEmits<{(e: 'error', err: unknown): void}>();

const dayLabels = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

function bitsFromMask(mask: number): boolean[] {
  return dayLabels.map((_, i) => (mask & (1 << i)) !== 0);
}

function maskFromBits(bits: boolean[]): number {
  return bits.reduce((mask, on, i) => (on ? mask | (1 << i) : mask), 0);
}

const rows = ref<CapacityRow[]>([]);
const editingId = ref(0);
const edit = reactive({hoursPerDay: 8, utilization: 0.8, workdays: bitsFromMask(62)});
const draft = reactive({login: '', hoursPerDay: 8, utilization: 0.8, workdays: bitsFromMask(62)});

function ownScope(row: CapacityRow): boolean {
  if (props.scope.repoId) return row.source === 'repo';
  if (props.scope.orgId) return row.source === 'org';
  return row.source === 'instance';
}

async function load() {
  try {
    rows.value = await getCapacity(props.config, props.scope);
  } catch (err) {
    emit('error', err);
  }
}

async function onCreate() {
  try {
    const userId = await getUserIDByLogin(props.config, draft.login);
    await setCapacityUser(props.config, userId, {
      repo_id: props.scope.repoId, org_id: props.scope.orgId,
      hours_per_day: draft.hoursPerDay, utilization: draft.utilization, workdays: maskFromBits(draft.workdays),
    });
    draft.login = '';
    await load();
  } catch (err) {
    emit('error', err);
  }
}

function startEdit(row: CapacityRow) {
  editingId.value = row.user_id;
  Object.assign(edit, {hoursPerDay: row.hours_per_day, utilization: row.utilization, workdays: bitsFromMask(row.workdays)});
}

async function onSaveEdit(row: CapacityRow) {
  try {
    await setCapacityUser(props.config, row.user_id, {
      repo_id: props.scope.repoId, org_id: props.scope.orgId,
      hours_per_day: edit.hoursPerDay, utilization: edit.utilization, workdays: maskFromBits(edit.workdays),
    });
    editingId.value = 0;
    await load();
  } catch (err) {
    emit('error', err);
  }
}

async function onClear(row: CapacityRow) {
  try {
    await clearCapacityUser(props.config, row.user_id, props.scope);
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
        <tr><th>User</th><th>Hours/day</th><th>Utilization</th><th>Workdays</th><th>Source</th><th v-if="canWrite"/></tr>
      </thead>
      <tbody>
        <tr v-for="row in rows" :key="row.user_id">
          <template v-if="editingId === row.user_id">
            <td>{{ row.login }}</td>
            <td><input v-model.number="edit.hoursPerDay" type="number" step="0.5" min="0" max="24" class="tw-w-20"></td>
            <td><input v-model.number="edit.utilization" type="number" step="0.05" min="0" max="1" class="tw-w-20"></td>
            <td class="tw-flex tw-gap-2 tw-flex-wrap">
              <label v-for="(label, i) in dayLabels" :key="label" class="tw-flex tw-items-center tw-gap-1">
                <input v-model="edit.workdays[i]" type="checkbox">{{ label }}
              </label>
            </td>
            <td>{{ row.source }}</td>
            <td>
              <button type="button" class="ui mini primary button" @click="onSaveEdit(row)">Save</button>
              <button type="button" class="ui mini button" @click="editingId = 0">Cancel</button>
            </td>
          </template>
          <template v-else>
            <td>{{ row.login }}</td>
            <td>{{ row.hours_per_day }}</td>
            <td>{{ row.utilization }}</td>
            <td>{{ dayLabels.filter((_, i) => bitsFromMask(row.workdays)[i]).join(', ') }}</td>
            <td>{{ row.source }}</td>
            <td v-if="canWrite">
              <template v-if="ownScope(row)">
                <button type="button" class="ui mini button" @click="startEdit(row)">Edit</button>
                <button type="button" class="ui mini negative button" @click="onClear(row)">Clear</button>
              </template>
            </td>
          </template>
        </tr>
      </tbody>
    </table>

    <form v-if="canWrite" class="ui form tw-flex tw-items-end tw-gap-2 tw-flex-wrap" @submit.prevent="onCreate">
      <div class="field"><label>User login<input v-model="draft.login" required></label></div>
      <div class="field"><label>Hours/day<input v-model.number="draft.hoursPerDay" type="number" step="0.5" min="0" max="24" class="tw-w-20"></label></div>
      <div class="field"><label>Utilization<input v-model.number="draft.utilization" type="number" step="0.05" min="0" max="1" class="tw-w-20"></label></div>
      <div class="field tw-flex tw-gap-2 tw-flex-wrap">
        <label v-for="(label, i) in dayLabels" :key="label" class="tw-flex tw-items-center tw-gap-1">
          <input v-model="draft.workdays[i]" type="checkbox">{{ label }}
        </label>
      </div>
      <button type="submit" class="ui primary button">Set capacity</button>
    </form>
  </div>
</template>
