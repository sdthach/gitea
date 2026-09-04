<script lang="ts" setup>
import {computed, onBeforeUnmount, onMounted, reactive, ref, watch} from 'vue';
import {addTrackedTime, deleteTrackedTime, startStopwatch, stopStopwatch} from '../../api.ts';
import {isoDate} from '../../drag.ts';
import {elapsedLabel, formatDurationSeconds, parseDurationSeconds} from '../../duration.ts';
import {shiftWeek, weekBounds} from '../../week.ts';
import type {PlanningStore} from '../../store.ts';
import type {TimesheetDay, TimesheetEntry, TimesheetLane} from '../../types.ts';
import AvatarImg from '../AvatarImg.vue';

const props = defineProps<{store: PlanningStore; canEditIssues: boolean; at: string}>();
const emit = defineEmits<{(e: 'update:at', value: string): void}>();

function todayUnix(): number {
  const now = new Date();
  return Math.floor(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()) / 1000);
}

const atUnix = computed(() => {
  if (!props.at) return todayUnix();
  const parsed = Date.parse(`${props.at}T00:00:00Z`);
  return Number.isNaN(parsed) ? todayUnix() : Math.floor(parsed / 1000);
});

const week = computed(() => weekBounds(atUnix.value));

const [owner, repoName] = computed(() => props.store.state.config.repoFullName.split('/')).value;

function loadWeek() {
  void props.store.loadTimesheet({from: isoDate(week.value.startUnix), to: isoDate(week.value.endUnix)});
}

watch(week, loadWeek, {immediate: true});
onMounted(() => props.store.loadCurrentUser());

function goToWeek(weeks: number) {
  emit('update:at', isoDate(shiftWeek(atUnix.value, weeks)));
}

const timesheet = computed(() => props.store.state.timesheet);
const lanes = computed<TimesheetLane[]>(() => timesheet.value?.lanes ?? []);
const dayColumns = computed(() => lanes.value[0]?.days.map((d) => d.unix) ?? []);

function dayLabel(unix: number): string {
  return new Date(unix * 1000).toLocaleDateString(undefined, {weekday: 'short', month: 'short', day: 'numeric', timeZone: 'UTC'});
}

function laneDay(lane: TimesheetLane, dayUnix: number): TimesheetDay | undefined {
  return lane.days.find((d) => d.unix === dayUnix);
}

function laneEditable(lane: TimesheetLane): boolean {
  return props.canEditIssues || lane.login === props.store.state.currentUserLogin;
}

// --- Timer bar: the doer's own running stopwatch, or a select over the roadmap's open issues.

const myRunning = computed(() => timesheet.value?.running.find((r) => r.login === props.store.state.currentUserLogin));

const nowTick = ref(Math.floor(Date.now() / 1000));
let tickTimer: ReturnType<typeof setInterval> | null = null;
onMounted(() => { tickTimer = setInterval(() => { nowTick.value = Math.floor(Date.now() / 1000); }, 1000); });
onBeforeUnmount(() => { if (tickTimer) clearInterval(tickTimer); });

const runningElapsed = computed(() => myRunning.value ? elapsedLabel(myRunning.value.started_unix, nowTick.value) : '');

// startableIssues offers the roadmap's own open bars plus its unmanaged rows: every issue the
// page already knows about, whether or not ccpm considers it "managed" enough for a bar.
const startableIssues = computed(() => {
  const roadmap = props.store.state.roadmap;
  if (!roadmap) return [];
  const seen = new Set<number>();
  const out: Array<{number: number; title: string}> = [];
  for (const bar of roadmap.bars) {
    if (!bar.is_closed && !seen.has(bar.number)) { seen.add(bar.number); out.push({number: bar.number, title: bar.title}); }
  }
  for (const item of roadmap.unmanaged) {
    if (!item.is_closed && !seen.has(item.number)) { seen.add(item.number); out.push({number: item.number, title: item.title}); }
  }
  return out;
});

const startSelection = ref(0);

async function onStartTimer() {
  if (!startSelection.value) return;
  const number = startSelection.value;
  await props.store.applyOptimistic(() => {}, async () => {
    await startStopwatch(props.store.state.config, owner, repoName, number);
    loadWeek();
  });
}

async function onStopTimer() {
  if (!myRunning.value) return;
  const number = myRunning.value.number;
  await props.store.applyOptimistic(() => {}, async () => {
    await stopStopwatch(props.store.state.config, owner, repoName, number);
    loadWeek();
  });
}

function issueUrl(number: number): string {
  return `/${props.store.state.config.repoFullName}/issues/${number}`;
}

// --- Add / edit entry form: one cell at a time.

const entryForm = reactive<{active: boolean; userId: number; dayUnix: number; issueNumber: number; duration: string; editing: TimesheetEntry | null}>({
  active: false, userId: 0, dayUnix: 0, issueNumber: 0, duration: '', editing: null,
});

function openAdd(lane: TimesheetLane, dayUnix: number) {
  if (!laneEditable(lane)) return;
  entryForm.active = true;
  entryForm.userId = lane.user_id;
  entryForm.dayUnix = dayUnix;
  entryForm.issueNumber = startableIssues.value[0]?.number ?? 0;
  entryForm.duration = '';
  entryForm.editing = null;
}

function openEdit(lane: TimesheetLane, dayUnix: number, entry: TimesheetEntry) {
  if (!props.canEditIssues || !entry.editable) return;
  entryForm.active = true;
  entryForm.userId = lane.user_id;
  entryForm.dayUnix = dayUnix;
  entryForm.issueNumber = entry.number;
  entryForm.duration = formatDurationSeconds(entry.seconds);
  entryForm.editing = entry;
}

function cancelForm() {
  entryForm.active = false;
  entryForm.editing = null;
}

async function submitForm() {
  const seconds = parseDurationSeconds(entryForm.duration);
  if (!seconds || !entryForm.issueNumber) { cancelForm(); return; }
  const created = `${isoDate(entryForm.dayUnix)}T00:00:00Z`;
  const editing = entryForm.editing;
  const issueNumber = entryForm.issueNumber;
  cancelForm();
  await props.store.applyOptimistic(() => {}, async () => {
    // An edit is a delete of the old entry followed by an add of the new one: Gitea's own
    // tracked-time API has no update, only add and delete.
    if (editing) await deleteTrackedTime(props.store.state.config, owner, repoName, editing.number, editing.id);
    await addTrackedTime(props.store.state.config, owner, repoName, issueNumber, {time: seconds, created});
    loadWeek();
  });
}

async function onDeleteEntry(entry: TimesheetEntry) {
  if (!props.canEditIssues || !entry.editable) return;
  await props.store.applyOptimistic(() => {}, async () => {
    await deleteTrackedTime(props.store.state.config, owner, repoName, entry.number, entry.id);
    loadWeek();
  });
}

const totals = computed(() => timesheet.value?.totals);
</script>

<template>
  <div class="planning-time tw-flex tw-flex-col tw-gap-4">
    <div class="tw-flex tw-items-center tw-gap-2 tw-flex-wrap">
      <button type="button" class="ui button" @click="goToWeek(-1)">‹ Previous week</button>
      <span>{{ isoDate(week.startUnix) }} – {{ isoDate(week.endUnix) }}</span>
      <button type="button" class="ui button" @click="goToWeek(1)">Next week ›</button>
    </div>

    <div class="planning-time-timer ui segment tw-flex tw-items-center tw-gap-2 tw-flex-wrap">
      <template v-if="myRunning">
        <span>Tracking</span>
        <a :href="issueUrl(myRunning.number)">{{ myRunning.title }} <span class="tw-text-text-light">#{{ myRunning.number }}</span></a>
        <span data-timer-elapsed class="tw-font-mono">{{ runningElapsed }}</span>
        <button type="button" class="ui tiny button" @click="onStopTimer">Stop</button>
      </template>
      <template v-else-if="canEditIssues">
        <label>Start timer
          <select v-model.number="startSelection" class="ui dropdown">
            <option :value="0" disabled>Select an issue…</option>
            <option v-for="issue in startableIssues" :key="issue.number" :value="issue.number">{{ issue.title }} #{{ issue.number }}</option>
          </select>
        </label>
        <button type="button" class="ui tiny button" :disabled="!startSelection" @click="onStartTimer">Start</button>
      </template>
    </div>

    <div class="tw-overflow-x-auto">
      <table class="planning-time-grid ui very basic table">
        <thead>
          <tr>
            <th>User</th>
            <th v-for="dayUnix in dayColumns" :key="dayUnix">{{ dayLabel(dayUnix) }}</th>
            <th>Total</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="lane in lanes" :key="lane.user_id">
            <td class="tw-flex tw-items-center tw-gap-1">
              <AvatarImg :login="lane.login" :size="16" :avatar-url="lane.avatar_url"/>
              {{ lane.display_name || lane.login }}
            </td>
            <td
              v-for="dayUnix in dayColumns" :key="dayUnix" :data-time-cell="`${lane.user_id}:${isoDate(dayUnix)}`"
              class="tw-cursor-pointer" @click="openAdd(lane, dayUnix)"
            >
              <span v-if="laneDay(lane, dayUnix)?.seconds">{{ formatDurationSeconds(laneDay(lane, dayUnix)!.seconds) }}</span>
              <div v-for="entry in laneDay(lane, dayUnix)?.entries ?? []" :key="entry.id" class="tw-text-xs tw-flex tw-items-center tw-gap-1">
                <span class="tw-cursor-pointer tw-underline" @click.stop="openEdit(lane, dayUnix, entry)">#{{ entry.number }}</span>
                <span v-if="canEditIssues && entry.editable" class="tw-cursor-pointer" @click.stop="onDeleteEntry(entry)">×</span>
              </div>
            </td>
            <td>{{ formatDurationSeconds(lane.total_seconds) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <form v-if="entryForm.active" class="ui form tw-flex tw-items-center tw-gap-2" @submit.prevent="submitForm">
      <select v-model.number="entryForm.issueNumber">
        <option v-for="issue in startableIssues" :key="issue.number" :value="issue.number">{{ issue.title }} #{{ issue.number }}</option>
      </select>
      <input v-model="entryForm.duration" placeholder="1h 30m" autofocus>
      <button type="submit" class="ui tiny primary button">{{ entryForm.editing ? 'Save' : 'Add' }}</button>
      <button type="button" class="ui tiny button" @click="cancelForm">Cancel</button>
    </form>

    <div v-if="totals" class="planning-time-totals tw-grid tw-grid-cols-1 md:tw-grid-cols-4 tw-gap-4">
      <div>
        <h5>By issue</h5>
        <div v-for="t in totals.by_issue" :key="t.issue_id">{{ t.title }} #{{ t.number }}: {{ formatDurationSeconds(t.seconds) }}</div>
      </div>
      <div>
        <h5>By user</h5>
        <div v-for="t in totals.by_user" :key="t.user_id">{{ t.login }}: {{ formatDurationSeconds(t.seconds) }}</div>
      </div>
      <div>
        <h5>By milestone</h5>
        <div v-for="t in totals.by_milestone" :key="t.milestone_id">{{ t.title }}: {{ formatDurationSeconds(t.seconds) }}</div>
      </div>
      <div>
        <h5>By type</h5>
        <div v-for="t in totals.by_type" :key="t.type_id">{{ t.name }}: {{ formatDurationSeconds(t.seconds) }}</div>
      </div>
    </div>
  </div>
</template>
