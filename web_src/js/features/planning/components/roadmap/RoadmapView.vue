<script lang="ts" setup>
import {computed, onBeforeUnmount, onMounted, reactive, ref, watch} from 'vue';
import {PX_PER_DAY, ticks, unixAtX, visibleWindow, weekendDayIndexes, xOf} from '../../scale.ts';
import type {Scale} from '../../scale.ts';
import {isoDate} from '../../drag.ts';
import type {DragWrite, RowGeometry, RowKind} from '../../drag.ts';
import {addIssueDependency, createIssue, removeIssueDependency, setIssueDates, setIssueGroup, setIssueMilestone, setIssueParent, setMilestoneSchedule} from '../../api.ts';
import {groupKey, orderGroupKeys} from '../../groups.ts';
import {treeOrder, visibleRows} from '../../tree.ts';
import {hasKnownStart} from '../../schedule.ts';
import {allowedChildTypes, defaultChildType} from '../../hierarchy.ts';
import type {ArrowRect} from '../../arrows.ts';
import type {PlanningStore} from '../../store.ts';
import type {Arrow, Bar, IssueType, RoadmapBarModel} from '../../types.ts';
import SvgIcon from '../../../../components/SvgIcon.vue';
import type {SvgName} from '../../../../svg.ts';
import RoadmapAxis from './RoadmapAxis.vue';
import RoadmapArrows from './RoadmapArrows.vue';
import RoadmapBar from './RoadmapBar.vue';
import SprintBands from './SprintBands.vue';
import RollupBracket from './RollupBracket.vue';
import CapacityStrip from './CapacityStrip.vue';
import UnscheduledPanel from './UnscheduledPanel.vue';
import AvatarImg from '../AvatarImg.vue';

const props = defineProps<{
  store: PlanningStore;
  canEditIssues: boolean;
  scale: string;
  at: string;
  groupBy: string;
  collapsed: number[];
}>();

const emit = defineEmits<{
  (e: 'update:scale', value: string): void;
  (e: 'update:at', value: string): void;
  (e: 'update:group-by', value: string): void;
  (e: 'toggle-collapse', issueId: number): void;
}>();

const ROW_HEIGHT = 32;
const HEADER_HEIGHT = 28;
const AXIS_HEIGHT = 32;

const containerEl = ref<HTMLElement | null>(null);
const widthPx = ref(1200);
let resizeObserver: ResizeObserver | null = null;

function measure() {
  if (containerEl.value) widthPx.value = containerEl.value.clientWidth || widthPx.value;
}

onMounted(() => {
  measure();
  resizeObserver = new ResizeObserver(measure);
  if (containerEl.value) resizeObserver.observe(containerEl.value);
});
onBeforeUnmount(() => resizeObserver?.disconnect());

const scaleValue = computed<Scale>(() => {
  return (['day', 'week', 'month', 'quarter'] as const).includes(props.scale as Scale) ? (props.scale as Scale) : 'week';
});

function todayUnix(): number {
  const now = new Date();
  return Math.floor(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()) / 1000);
}

const atUnix = computed(() => {
  if (!props.at) return todayUnix();
  const parsed = Date.parse(`${props.at}T00:00:00Z`);
  return Number.isNaN(parsed) ? todayUnix() : Math.floor(parsed / 1000);
});

const windowValue = computed(() => visibleWindow(atUnix.value, scaleValue.value, widthPx.value));
const ticksList = computed(() => ticks(windowValue.value.origin, windowValue.value.days, scaleValue.value));
const contentWidth = computed(() => windowValue.value.days * PX_PER_DAY[scaleValue.value]);
const todayX = computed(() => {
  const t = todayUnix();
  if (t < windowValue.value.origin || t > windowValue.value.endUnix) return null;
  return xOf(t, windowValue.value.origin, scaleValue.value);
});

// weekendBands shades the same day columns behind every row that RoadmapAxis.vue shades behind
// its own ticks, at day and week scale only — a single column reads at coarser scales too.
const weekendBands = computed(() => (scaleValue.value === 'day' || scaleValue.value === 'week')
  ? weekendDayIndexes(windowValue.value.origin, windowValue.value.days).map((i) => ({x: i * PX_PER_DAY[scaleValue.value], width: PX_PER_DAY[scaleValue.value]}))
  : []);

const rowMode = computed<RowKind>(() => {
  return (props.groupBy === 'parent' || props.groupBy === 'milestone') ? props.groupBy : 'assignee';
});

const roadmap = computed(() => props.store.state.roadmap);
const bars = computed<Bar[]>(() => roadmap.value?.bars ?? []);
const milestones = computed(() => props.store.state.milestones);
const capacity = computed(() => props.store.state.capacity);
const collapsedSet = computed(() => new Set(props.collapsed));

watch(windowValue, (win) => {
  void props.store.loadCapacity({from: isoDate(win.origin), to: isoDate(win.endUnix)});
}, {immediate: true});

// barGroupInput mirrors board.ts's cardGroupInput: a bar groups under its root issue once it
// has children of its own, or is itself one of another bar's children.
function barGroupInput(bar: Bar) {
  return {
    typeName: bar.type, assignees: bar.assignees, rootIssueId: bar.root_issue_id,
    hasChildren: Boolean(bar.has_children) || (Boolean(bar.root_issue_id) && bar.root_issue_id !== bar.issue_id),
  };
}

function toBarModel(bar: Bar, rowKey: string): RoadmapBarModel {
  return {
    issueId: bar.issue_id, number: bar.number, title: bar.title, url: bar.url,
    startUnix: bar.start_unix, endUnix: bar.end_unix, endInferred: bar.end_inferred,
    typeColor: bar.type_color, typeIcon: bar.type_icon, rowKey,
  };
}

type LayoutRow = {
  key: string;
  kind: RowKind;
  label: string;
  count: number;
  top: number;
  height: number;
  isHeader: boolean;
  bar?: RoadmapBarModel;
  depth: number;
  capacityLogin?: string;
  rollupKey?: string;
  issueId?: number;
  hasChildren?: boolean;
};

// scheduledBars excludes a bar whose start is only inferred from when its issue was created:
// that bar belongs in the "Needs a start" panel alone, never drawn as a row on the timeline.
const scheduledBars = computed(() => bars.value.filter(hasKnownStart));

// layout arranges every visible row top to bottom for the active row mode: one lane header plus
// one row per bar under assignee and milestone, one row per issue in tree order under parent.
const layout = computed<LayoutRow[]>(() => {
  const rows: LayoutRow[] = [];
  let y = 0;
  const mode = rowMode.value;

  if (mode === 'parent') {
    const keyed = scheduledBars.value.map((bar) => ({bar, key: groupKey(barGroupInput(bar), 'parent')}));
    for (const rootKey of orderGroupKeys(keyed.map((entry) => entry.key))) {
      const rootBars = keyed.filter((entry) => entry.key === rootKey).map((entry) => entry.bar);
      const label = rootKey === '' ? 'No parent' : (bars.value.find((b) => String(b.issue_id) === rootKey)?.title ?? rootKey);
      rows.push({key: rootKey, kind: 'parent', label, count: rootBars.length, top: y, height: HEADER_HEIGHT, isHeader: true, depth: 0, rollupKey: rootKey ? `parent:${rootKey}` : undefined});
      y += HEADER_HEIGHT;
      const treeRows = rootBars.map((bar) => ({issueId: bar.issue_id, parentIssueId: bar.parent_issue_id, bar}));
      // childCounts is scoped to this root group, so the chevron only ever appears on a row
      // whose children are actually rows here — never on one whose only child sits elsewhere.
      const childCounts = new Map<number, number>();
      for (const row of treeRows) {
        if (row.parentIssueId) childCounts.set(row.parentIssueId, (childCounts.get(row.parentIssueId) ?? 0) + 1);
      }
      for (const row of visibleRows(treeOrder(treeRows), collapsedSet.value)) {
        rows.push({
          key: String(row.issueId), kind: 'parent', label: row.bar.title, count: 1,
          top: y, height: ROW_HEIGHT, isHeader: false, bar: toBarModel(row.bar, String(row.issueId)),
          depth: row.bar.depth ?? 0, issueId: row.issueId, hasChildren: (childCounts.get(row.issueId) ?? 0) > 0,
        });
        y += ROW_HEIGHT;
      }
    }
  } else {
    const laneOf = (bar: Bar) => (mode === 'milestone' ? String(bar.milestone_id ?? '') : groupKey(barGroupInput(bar), 'assignee'));
    const laneLabel = (key: string) => {
      if (mode === 'milestone') return key === '' ? 'No milestone' : (milestones.value.find((m) => String(m.milestone_id) === key)?.title ?? key);
      return key === '' ? 'Unassigned' : key;
    };
    const keyed = scheduledBars.value.map((bar) => ({bar, key: laneOf(bar)}));
    for (const laneKey of orderGroupKeys(keyed.map((entry) => entry.key))) {
      const laneBars = keyed.filter((entry) => entry.key === laneKey).map((entry) => entry.bar)
        .sort((a, b) => a.start_unix - b.start_unix);
      rows.push({
        key: laneKey, kind: mode, label: laneLabel(laneKey), count: laneBars.length, top: y,
        height: HEADER_HEIGHT, isHeader: true, depth: 0,
        capacityLogin: mode === 'assignee' ? laneKey : undefined,
        rollupKey: mode === 'milestone' && laneKey ? `milestone:${laneKey}` : undefined,
      });
      y += HEADER_HEIGHT;
      for (const bar of laneBars) {
        rows.push({key: laneKey, kind: mode, label: bar.title, count: 1, top: y, height: ROW_HEIGHT, isHeader: false, bar: toBarModel(bar, laneKey), depth: 0});
        y += ROW_HEIGHT;
      }
    }
  }
  return rows;
});

const totalHeight = computed(() => layout.value.reduce((max, row) => Math.max(max, row.top + row.height), 0));

const nonHeaderRows = computed(() => layout.value.filter((row) => !row.isHeader));

const rowGeometry = computed<RowGeometry>(() => ({
  rowHeight: ROW_HEIGHT,
  rows: nonHeaderRows.value.map((row) => ({key: row.key, kind: row.kind, top: row.top})),
}));

// barRows pairs each bar row with its own index into rowGeometry.rows, so a bar can pass that
// index straight to RoadmapBar rather than the row having to be found again by key — a lane
// holding several bars shares one key among rows, so only the index picks out one of them.
const barRows = computed(() => nonHeaderRows.value
  .map((row, rowIndex) => ({row, rowIndex}))
  .filter((entry): entry is {row: LayoutRow & {bar: RoadmapBarModel}; rowIndex: number} => !!entry.row.bar));

// rollupByKey is addressed the same way the server names a rollup (RollupRow.RollupKey: kind
// + ":" + key), so a parent or milestone bracket resolves instead of missing by key alone.
const rollupByKey = computed(() => new Map((roadmap.value?.rollups ?? []).map((rollup) => [`${rollup.kind}:${rollup.key}`, rollup])));

// arrowGeometry is every drawn bar's own rect, keyed by issue id — the same rect RoadmapBar
// itself draws at, so an arrow lands exactly on the bar's edge rather than a second, drifting
// computation of it.
const arrowGeometry = computed<Map<string, ArrowRect>>(() => {
  const map = new Map<string, ArrowRect>();
  for (const entry of barRows.value) {
    const bar = entry.row.bar;
    const x = xOf(bar.startUnix, windowValue.value.origin, scaleValue.value);
    const width = Math.max(1, xOf(bar.endUnix, windowValue.value.origin, scaleValue.value) - x);
    map.set(String(bar.issueId), {x, y: AXIS_HEIGHT + entry.row.top + 2, width, height: entry.row.height - 4});
  }
  return map;
});

const arrows = computed<Arrow[]>(() => roadmap.value?.arrows ?? []);

async function onArrowRemove(arrow: Arrow) {
  const config = props.store.state.config;
  await props.store.applyOptimistic(
    () => {
      const before = roadmap.value;
      if (!before) return;
      const kept = before.arrows.filter((a) => a !== arrow);
      before.arrows = kept;
      return () => { before.arrows = [...before.arrows, arrow]; };
    },
    async () => {
      const result = await removeIssueDependency(config, arrow.to_issue_id, arrow.from_issue_id, config.repoFullName);
      props.store.setRoadmap(result);
    },
  );
}

async function onArrowLink(payload: {fromIssueId: number; toIssueId: number}) {
  if (!props.canEditIssues) return;
  const config = props.store.state.config;
  await props.store.applyOptimistic(() => {}, async () => {
    const result = await addIssueDependency(config, payload.toIssueId, {repo: config.repoFullName, depends_on_issue_id: payload.fromIssueId});
    props.store.setRoadmap(result);
  });
}

function capacityLane(login: string) {
  return capacity.value?.lanes.find((lane) => lane.login === login);
}

async function issueWrite(issueId: number, write: DragWrite) {
  const config = props.store.state.config;
  const repo = config.repoFullName;
  switch (write.kind) {
    case 'dates':
      return setIssueDates(config, issueId, {repo, start: write.start, end: write.end});
    case 'group':
      return setIssueGroup(config, issueId, {repo, group_by: 'assignee', group: write.group});
    case 'milestone':
      return setIssueMilestone(config, issueId, {repo, milestone_id: write.milestoneId});
    case 'parent':
      await setIssueParent(config, issueId, {repo, parent_issue_id: write.parentIssueId});
      return undefined;
  }
}

function findBar(issueId: number): Bar | undefined {
  return roadmap.value?.bars.find((bar) => bar.issue_id === issueId);
}

function applyWritesLocally(bar: Bar, writes: DragWrite[]): () => void {
  const previous = {...bar};
  for (const write of writes) {
    if (write.kind === 'dates') {
      bar.start_unix = Math.floor(Date.parse(`${write.start}T00:00:00Z`) / 1000);
      bar.end_unix = Math.floor(Date.parse(`${write.end}T00:00:00Z`) / 1000);
      bar.start_source = 'schedule';
      bar.end_inferred = false;
    } else if (write.kind === 'group') {
      bar.assignees = write.group ? [write.group] : [];
    } else if (write.kind === 'milestone') {
      bar.milestone_id = write.milestoneId || undefined;
    } else if (write.kind === 'parent') {
      bar.parent_issue_id = write.parentIssueId;
    }
  }
  return () => Object.assign(bar, previous);
}

// Each write commits and reverts on its own, so a group write failing after a dates write already succeeded leaves the dates in place.
async function onBarCommit(issueId: number, writes: DragWrite[]) {
  if (!writes.length) return;
  const bar = findBar(issueId);
  if (!bar) return;
  for (const write of writes) {
    await props.store.applyOptimistic(
      () => applyWritesLocally(bar, [write]),
      async () => {
        const response = await issueWrite(issueId, write);
        if (response) props.store.setRoadmap(response);
        if (write.kind === 'parent') await props.store.loadRoadmap();
      },
    );
  }
}

async function onScheduleMilestone(payload: {milestoneId: number; start: string}) {
  const config = props.store.state.config;
  await props.store.applyOptimistic(
    () => {},
    async () => {
      await setMilestoneSchedule(config, payload.milestoneId, {repo: config.repoFullName, start: payload.start});
      await props.store.loadRoadmap();
    },
  );
}

const creating = reactive<{active: boolean; rowKey: string; rowKind: RowKind; dayUnix: number; title: string; typeId: number}>({
  active: false, rowKey: '', rowKind: 'assignee', dayUnix: 0, title: '', typeId: 0,
});

// parentRankFor resolves a parent row's own rank from its bar's assigned type, absent when the
// row has no bar (an empty "No parent" group) or the bar carries no type to rank it by.
function parentRankFor(rowKey: string): number | undefined {
  const parentBar = bars.value.find((b) => String(b.issue_id) === rowKey);
  if (parentBar?.type_id === undefined) return undefined;
  return roadmap.value?.types.find((t) => t.id === parentBar.type_id)?.rank;
}

// creatingAllowedTypes is non-empty only for a parent row whose type outranks at least one
// other visible type; every other row's create form carries no type select at all.
const creatingAllowedTypes = computed<IssueType[]>(() => {
  if (creating.rowKind !== 'parent' || !creating.rowKey) return [];
  const parentRank = parentRankFor(creating.rowKey);
  return parentRank === undefined ? [] : allowedChildTypes(roadmap.value?.types ?? [], parentRank);
});

function openCreate(row: LayoutRow, offsetX: number) {
  if (!props.canEditIssues) return;
  let typeId = 0;
  if (row.kind === 'parent' && row.key) {
    const parentRank = parentRankFor(row.key);
    const defaultType = parentRank === undefined ? undefined : defaultChildType(roadmap.value?.types ?? [], parentRank);
    if (!defaultType) return; // no allowed child type: the create affordance stays hidden on this row
    typeId = defaultType.id;
  }
  creating.active = true;
  creating.rowKey = row.key;
  creating.rowKind = row.kind;
  creating.dayUnix = unixAtX(offsetX, windowValue.value.origin, scaleValue.value);
  creating.title = '';
  creating.typeId = typeId;
}

function cancelCreate() {
  creating.active = false;
}

function submitCreate() {
  const title = creating.title.trim();
  if (!title) { cancelCreate(); return; }
  const config = props.store.state.config;
  const body: Parameters<typeof createIssue>[1] = {
    repo: config.repoFullName, title, start: isoDate(creating.dayUnix), end: isoDate(creating.dayUnix + 86400),
  };
  if (creating.typeId) body.type_id = creating.typeId;
  if (creating.rowKind === 'milestone') {
    if (creating.rowKey) body.milestone_id = Number(creating.rowKey);
  } else if (creating.rowKind === 'assignee') {
    body.group_by = 'assignee';
    body.group = creating.rowKey;
  } else if (creating.rowKey) {
    body.group_by = 'parent';
    body.group = creating.rowKey;
  }
  cancelCreate();
  props.store.applyOptimistic(() => {}, async () => {
    const result = await createIssue(config, body);
    props.store.setRoadmap(result);
  });
}

function onRowDrop(row: LayoutRow, event: DragEvent) {
  event.preventDefault();
  if (!props.canEditIssues) return;
  const issueId = Number(event.dataTransfer!.getData('text/plain'));
  if (!issueId) return;
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
  const dayUnix = unixAtX(event.clientX - rect.left, windowValue.value.origin, scaleValue.value);
  const start = isoDate(dayUnix);
  const end = isoDate(dayUnix + 86400);
  const config = props.store.state.config;
  const repo = config.repoFullName;
  props.store.applyOptimistic(() => {}, async () => {
    let result = await setIssueDates(config, issueId, {repo, start, end});
    if (row.kind === 'assignee') result = await setIssueGroup(config, issueId, {repo, group_by: 'assignee', group: row.key});
    else if (row.kind === 'milestone') result = await setIssueMilestone(config, issueId, {repo, milestone_id: Number(row.key) || 0});
    else if (row.kind === 'parent' && row.key) { await setIssueParent(config, issueId, {repo, parent_issue_id: Number(row.key)}); await props.store.loadRoadmap(); return; }
    props.store.setRoadmap(result);
  });
}

// avatarByLogin pools every bar's own assignee_avatars, so a lane header can look one up by
// login without a second fetch — the same avatar url the bars in that lane already carry.
const avatarByLogin = computed(() => {
  const map = new Map<string, string>();
  for (const bar of bars.value) {
    for (const avatar of bar.assignee_avatars) map.set(avatar.login, avatar.avatar_url);
  }
  return map;
});

const unmanaged = computed(() => roadmap.value?.unmanaged ?? []);
const unscheduledBars = computed(() => bars.value.filter((bar) => bar.start_source === 'issue_created')
  .map((bar) => ({issue_id: bar.issue_id, number: bar.number, title: bar.title, url: bar.url})));
</script>

<template>
  <div class="planning-roadmap tw-flex tw-flex-col tw-gap-2">
    <div class="tw-flex tw-items-center tw-gap-2 tw-flex-wrap">
      <label>Scale
        <select class="ui dropdown" :value="scaleValue" @change="emit('update:scale', ($event.target as HTMLSelectElement).value)">
          <option value="day">Day</option>
          <option value="week">Week</option>
          <option value="month">Month</option>
          <option value="quarter">Quarter</option>
        </select>
      </label>
      <label>Rows
        <select class="ui dropdown" :value="rowMode" @change="emit('update:group-by', ($event.target as HTMLSelectElement).value)">
          <option value="assignee">Assignee</option>
          <option value="parent">Parent</option>
          <option value="milestone">Milestone</option>
        </select>
      </label>
      <button type="button" class="ui button" @click="emit('update:at', isoDate(todayUnix()))">Today</button>
    </div>

    <div class="tw-flex tw-gap-4">
      <div ref="containerEl" class="planning-roadmap-body tw-flex tw-overflow-x-auto tw-flex-1">
        <div class="planning-roadmap-headers tw-w-48 tw-flex-shrink-0 tw-sticky tw-left-0 tw-bg-body tw-z-10">
          <div :style="{height: `${AXIS_HEIGHT}px`}"/>
          <div
            v-for="(row, index) in layout" :key="`h-${index}`"
            class="tw-flex tw-items-center tw-gap-1 tw-truncate tw-text-sm"
            :class="{'tw-font-semibold': row.isHeader}"
            :style="{height: `${row.height}px`, paddingLeft: row.isHeader ? 0 : `${(row.depth + 1) * 0.75}rem`}"
          >
            <span
              v-if="row.hasChildren" class="tw-flex-shrink-0 tw-cursor-pointer"
              role="button" :aria-label="collapsedSet.has(row.issueId!) ? 'Expand' : 'Collapse'"
              @click="emit('toggle-collapse', row.issueId!)"
            ><svg-icon :name="(collapsedSet.has(row.issueId!) ? 'octicon-chevron-right' : 'octicon-chevron-down') as SvgName"/></span>
            <AvatarImg v-if="row.isHeader && row.kind === 'assignee' && row.key" :login="row.key" :size="16" :avatar-url="avatarByLogin.get(row.key)"/>
            <span class="tw-truncate">{{ row.label }}<span v-if="row.isHeader" class="tw-text-text-light"> ({{ row.count }})</span></span>
          </div>
        </div>

        <div class="planning-roadmap-timeline tw-relative" :style="{width: `${contentWidth}px`}">
          <span
            v-for="band in weekendBands" :key="`weekend-row-${band.x}`" class="tw-absolute tw-pointer-events-none tw-bg-hover"
            :style="{left: `${band.x}px`, width: `${band.width}px`, top: `${AXIS_HEIGHT}px`, height: `${totalHeight}px`}"
          />
          <RoadmapAxis :ticks="ticksList" :width-px="contentWidth" :today-x="todayX" :origin="windowValue.origin" :days="windowValue.days" :scale="scaleValue"/>
          <SprintBands
            :milestones="milestones" :sprints="capacity?.sprints ?? []" :origin="windowValue.origin"
            :scale="scaleValue" :height-px="totalHeight" :can-edit-issues="canEditIssues"
            @schedule="onScheduleMilestone"
          />
          <span
            v-if="todayX !== null" class="tw-absolute tw-w-0.5 tw-bg-red tw-pointer-events-none"
            :style="{left: `${todayX}px`, top: 0, height: `${AXIS_HEIGHT + totalHeight}px`}"
          />

          <div
            v-for="(row, index) in layout" :key="`row-${index}`"
            class="planning-roadmap-row tw-absolute tw-inset-x-0"
            :style="{top: `${AXIS_HEIGHT + row.top}px`, height: `${row.height}px`}"
            @click="openCreate(row, $event.offsetX)"
            @dragover.prevent
            @drop="onRowDrop(row, $event)"
          >
            <CapacityStrip v-if="row.capacityLogin !== undefined" :lane="capacityLane(row.capacityLogin)" :origin="windowValue.origin" :scale="scaleValue"/>
            <RollupBracket
              v-if="row.rollupKey && rollupByKey.get(row.rollupKey)" :rollup="rollupByKey.get(row.rollupKey)!"
              :origin="windowValue.origin" :scale="scaleValue" :top="4" :height="row.height - 8"
            />
            <form
              v-if="creating.active && creating.rowKey === row.key && creating.rowKind === row.kind"
              class="tw-absolute tw-z-20 tw-flex tw-gap-1" :style="{left: `${xOf(creating.dayUnix, windowValue.origin, scaleValue)}px`, top: '2px'}"
              @submit.prevent="submitCreate"
            >
              <select v-if="creatingAllowedTypes.length" v-model.number="creating.typeId" class="tw-text-xs">
                <option v-for="type in creatingAllowedTypes" :key="type.id" :value="type.id">{{ type.name }}</option>
              </select>
              <input v-model="creating.title" autofocus class="tw-text-xs" placeholder="Title" @keydown.escape="cancelCreate" @blur="submitCreate">
            </form>
          </div>

          <RoadmapArrows
            :arrows="arrows" :geometry="arrowGeometry" :width-px="contentWidth" :height-px="AXIS_HEIGHT + totalHeight"
            :can-edit-issues="canEditIssues" @remove="onArrowRemove"
          />

          <RoadmapBar
            v-for="entry in barRows" :key="entry.row.bar.issueId"
            :bar="entry.row.bar" :origin="windowValue.origin" :scale="scaleValue" :row-geometry="rowGeometry"
            :row-index="entry.rowIndex" :can-edit-issues="canEditIssues" :top="AXIS_HEIGHT + entry.row.top + 2" :height="entry.row.height - 4"
            @commit="(writes) => onBarCommit(entry.row.bar.issueId, writes)"
            @link="onArrowLink"
          />
        </div>
      </div>

      <UnscheduledPanel :unmanaged="unmanaged" :unscheduled-bars="unscheduledBars" :can-edit-issues="canEditIssues"/>
    </div>
  </div>
</template>
