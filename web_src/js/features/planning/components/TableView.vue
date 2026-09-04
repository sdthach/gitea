<script lang="ts" setup>
import {computed, ref} from 'vue';
import SvgIcon from '../../../components/SvgIcon.vue';
import type {SvgName} from '../../../svg.ts';
import {setIssueMilestone, setIssueType, clearIssueType, setIssueDates, setIssueFields, setIssueEstimate} from '../api.ts';
import {filterRows} from '../filter.ts';
import type {FieldKind} from '../filter.ts';
import {groupKey, orderGroupKeys, emptyGroupLabel} from '../groups.ts';
import type {Grouping} from '../groups.ts';
import {treeOrder, visibleRows} from '../tree.ts';
import type {PlanningStore} from '../store.ts';
import type {Field} from '../types.ts';

const props = defineProps<{store: PlanningStore; groupBy: Grouping; query: string; collapsed: number[]}>();
const emit = defineEmits<{(e: 'toggle-collapse', issueId: number): void}>();

type Row = {
  issueId: number;
  number: number;
  title: string;
  url: string;
  isClosed: boolean;
  type?: string;
  typeIcon?: string;
  typeId?: number;
  columnId?: number;
  assignees: string[];
  labels: string[];
  milestone?: string;
  milestoneId?: number;
  parentIssueId?: number;
  rootIssueId?: number;
  hasChildren: boolean;
  startUnix?: number;
  endUnix?: number;
  points: number;
  fields: Record<string, unknown>;
  timeEstimate: number;
  trackedSeconds: number;
  depth: number;
};

function blankRow(issueId: number): Row {
  return {
    issueId, number: 0, title: '', url: '', isClosed: false, assignees: [], labels: [],
    hasChildren: false, points: 0, fields: {}, timeEstimate: 0, trackedSeconds: 0, depth: 0,
  };
}

const rows = computed<Row[]>(() => {
  const byId = new Map<number, Row>();
  function upsert(partial: Partial<Row> & {issueId: number}) {
    byId.set(partial.issueId, {...blankRow(partial.issueId), ...byId.get(partial.issueId), ...partial});
  }

  const board = props.store.state.board;
  if (board) {
    for (const group of board.groups) {
      for (const column of group.columns) {
        for (const card of column.cards) {
          upsert({
            issueId: card.issue_id, number: card.number, title: card.title, url: card.url,
            isClosed: card.is_closed, type: card.type, typeIcon: card.type_icon, typeId: card.type_id,
            columnId: card.column_id, assignees: card.assignees, labels: card.labels,
            milestone: card.milestone, milestoneId: card.milestone_id,
            parentIssueId: card.parent_issue_id, rootIssueId: card.root_issue_id, depth: card.depth ?? 0,
            hasChildren: card.has_children ?? false, points: card.points, fields: card.fields,
            timeEstimate: card.time_estimate, trackedSeconds: card.tracked_seconds,
          });
        }
      }
    }
  }

  const roadmap = props.store.state.roadmap;
  if (roadmap) {
    for (const bar of roadmap.bars) {
      upsert({
        issueId: bar.issue_id, number: bar.number, title: bar.title, url: bar.url,
        isClosed: bar.is_closed, type: bar.type, typeIcon: bar.type_icon, typeId: bar.type_id,
        assignees: bar.assignees ?? [], labels: bar.labels ?? [],
        milestone: bar.milestone, milestoneId: bar.milestone_id,
        parentIssueId: bar.parent_issue_id, rootIssueId: bar.root_issue_id, depth: bar.depth ?? 0,
        hasChildren: bar.has_children ?? false, startUnix: bar.start_unix, endUnix: bar.end_unix,
        points: bar.points, fields: bar.fields,
        timeEstimate: bar.time_estimate, trackedSeconds: bar.tracked_seconds,
      });
    }
    for (const item of roadmap.unmanaged) {
      upsert({
        issueId: item.issue_id, number: item.number, title: item.title, url: item.url,
        isClosed: item.is_closed, type: item.type, typeId: item.type_id,
        assignees: item.assignees ?? [], labels: item.labels ?? [],
        milestoneId: item.milestone_id, points: item.points, fields: item.fields,
        timeEstimate: item.time_estimate, trackedSeconds: item.tracked_seconds,
      });
    }
  }

  for (const row of byId.values()) {
    Object.assign(row, props.store.state.overrides[row.issueId]);
  }
  return [...byId.values()];
});

const rowById = computed(() => new Map(rows.value.map((row) => [row.issueId, row])));

const types = computed(() => props.store.state.board?.types ?? props.store.state.roadmap?.types ?? []);
const milestones = computed(() => props.store.state.roadmap?.milestones ?? []);
const fields = computed<Field[]>(() => props.store.state.board?.fields ?? props.store.state.roadmap?.fields ?? []);
const fieldKinds = computed<Record<string, FieldKind>>(
  () => Object.fromEntries(fields.value.map((field) => [field.key, field.kind as FieldKind])),
);
// Gated on Issues write, not Projects write: every editor here — type, milestone, dates,
// fields, estimate — edits the issue itself, and an empty token means the page could not mint
// one to write with regardless of what the doer may otherwise do.
const canEditIssues = computed(() => props.store.state.config.canEditIssues && !!props.store.state.config.token);
const isTreeMode = computed(() => props.groupBy === 'none' || props.groupBy === 'parent');
const collapsedSet = computed(() => new Set(props.collapsed));

function columnTitle(columnId?: number): string {
  return props.store.state.board?.columns.find((column) => column.column_id === columnId)?.title ?? '';
}

// Types' icons are free text an admin enters on the Types settings page, naming an octicon the
// bundle carries; a name outside the bundle is on the data, not on this cast.
function typeIconName(row: Row): SvgName | undefined {
  return row.typeIcon as SvgName | undefined;
}

function parentLabel(row: Row): string {
  if (!row.parentIssueId) return '';
  return `#${rowById.value.get(row.parentIssueId)?.number ?? row.parentIssueId}`;
}

function groupHasChildren(row: Row): boolean {
  return row.hasChildren || (!!row.rootIssueId && row.rootIssueId !== row.issueId);
}

const sortKey = ref('title');
const sortDir = ref<1 | -1>(1);

function accessor(row: Row, key: string): string | number {
  switch (key) {
    case 'title': return row.title.toLowerCase();
    case 'column': return columnTitle(row.columnId).toLowerCase();
    case 'type': return (row.type ?? '').toLowerCase();
    case 'parent': return row.parentIssueId ?? 0;
    case 'assignees': return (row.assignees[0] ?? '').toLowerCase();
    case 'milestone': return (row.milestone ?? '').toLowerCase();
    case 'start': return row.startUnix ?? 0;
    case 'end': return row.endUnix ?? 0;
    case 'estimate': return row.timeEstimate;
    case 'tracked': return row.trackedSeconds;
    case 'points': return row.points;
    default: return String(row.fields[key] ?? '').toLowerCase();
  }
}

function sortRows(list: Row[]): Row[] {
  return [...list].sort((a, b) => {
    const av = accessor(a, sortKey.value);
    const bv = accessor(b, sortKey.value);
    if (av < bv) return -1 * sortDir.value;
    if (av > bv) return 1 * sortDir.value;
    return 0;
  });
}

function onSort(key: string) {
  if (sortKey.value === key) {
    sortDir.value = sortDir.value === 1 ? -1 : 1;
  } else {
    sortKey.value = key;
    sortDir.value = 1;
  }
}

// orderRows arranges one group's own rows: outside tree mode, by whichever column the header
// click chose; in tree mode (no grouping, or grouping by parent, where every row in a group
// already shares one root), tree order groups each parent with its own children, indented and
// collapsible, and the column sort instead ranks siblings within that structure.
function orderRows(list: Row[]): Row[] {
  const sorted = sortRows(list);
  if (!isTreeMode.value) return sorted;
  return visibleRows(treeOrder(sorted), collapsedSet.value);
}

const groupedRows = computed(() => {
  const filtered = filterRows(rows.value, props.query, fieldKinds.value);
  const keyed = filtered.map((row) => ({
    row,
    key: groupKey({
      typeName: row.type, assignees: row.assignees, rootIssueId: row.rootIssueId,
      hasChildren: groupHasChildren(row),
    }, props.groupBy),
  }));
  return orderGroupKeys(keyed.map((entry) => entry.key)).map((key) => ({
    key,
    label: key === '' ? emptyGroupLabel(props.groupBy)
      : (props.groupBy === 'parent' ? (rowById.value.get(Number(key))?.title ?? key) : key),
    rows: orderRows(keyed.filter((entry) => entry.key === key).map((entry) => entry.row)),
  }));
});

// childCounts is scoped to the filtered set groupedRows itself renders, so a row whose only
// children a search query hides shows no expander for a subtree there is nothing to reveal.
const childCounts = computed(() => {
  const counts = new Map<number, number>();
  for (const row of filterRows(rows.value, props.query, fieldKinds.value)) {
    if (row.parentIssueId) counts.set(row.parentIssueId, (counts.get(row.parentIssueId) ?? 0) + 1);
  }
  return counts;
});

function isExpandable(row: Row): boolean {
  return isTreeMode.value && (childCounts.value.get(row.issueId) ?? 0) > 0;
}

function isCollapsed(row: Row): boolean {
  return collapsedSet.value.has(row.issueId);
}

function toggleCollapse(row: Row) {
  emit('toggle-collapse', row.issueId);
}

const columnCount = computed(() => 11 + fields.value.length);

function unixToDateInput(unix?: number): string {
  return unix ? new Date(unix * 1000).toISOString().slice(0, 10) : '';
}

function formatHours(seconds: number): string {
  return seconds ? `${Math.round((seconds / 3600) * 10) / 10}h` : '—';
}

// estimateInputValue renders the recorded estimate the way the endpoint's own body reads it
// back: days and hours, so what a client sees round-trips through a resubmit unchanged.
function estimateInputValue(seconds: number): string {
  if (!seconds) return '';
  const days = Math.floor(seconds / 86400);
  const hours = Math.round((seconds % 86400) / 3600);
  return [days ? `${days}d` : '', hours || !days ? `${hours}h` : ''].filter(Boolean).join(' ');
}

function onMilestoneChange(row: Row, event: Event) {
  const milestoneId = Number((event.target as HTMLSelectElement).value);
  const milestone = milestones.value.find((m) => m.milestone_id === milestoneId)?.title;
  const previous = {milestoneId: row.milestoneId, milestone: row.milestone};
  props.store.applyOptimistic(
    () => {
      props.store.setOverride(row.issueId, {milestoneId: milestoneId || undefined, milestone});
      return () => props.store.setOverride(row.issueId, previous);
    },
    () => setIssueMilestone(props.store.state.config, row.issueId, {
      repo: props.store.state.config.repoFullName, milestone_id: milestoneId,
    }),
  );
}

function onTypeChange(row: Row, event: Event) {
  const typeId = Number((event.target as HTMLSelectElement).value);
  const type = types.value.find((t) => t.id === typeId);
  const previous = {typeId: row.typeId, type: row.type, typeIcon: row.typeIcon};
  const repo = props.store.state.config.repoFullName;
  props.store.applyOptimistic(
    () => {
      props.store.setOverride(row.issueId, {typeId: typeId || undefined, type: type?.name, typeIcon: type?.icon});
      return () => props.store.setOverride(row.issueId, previous);
    },
    () => (typeId
      ? setIssueType(props.store.state.config, row.issueId, {repo, type_id: typeId})
      : clearIssueType(props.store.state.config, row.issueId, repo)),
  );
}

function onDateChange(row: Row, field: 'start' | 'end', event: Event) {
  const value = (event.target as HTMLInputElement).value;
  const unixField = field === 'start' ? 'startUnix' : 'endUnix';
  const unix = value ? Math.floor(Date.parse(`${value}T00:00:00Z`) / 1000) : undefined;
  const previous = row[unixField];
  props.store.applyOptimistic(
    () => {
      props.store.setOverride(row.issueId, {[unixField]: unix});
      return () => props.store.setOverride(row.issueId, {[unixField]: previous});
    },
    () => setIssueDates(props.store.state.config, row.issueId, {
      repo: props.store.state.config.repoFullName, [field]: value,
    }),
  );
}

function onFieldChange(row: Row, field: Field, event: Event) {
  const value = (event.target as HTMLInputElement).value;
  const previous = row.fields[field.key];
  props.store.applyOptimistic(
    () => {
      props.store.setOverride(row.issueId, {fields: {...row.fields, [field.key]: value}});
      return () => props.store.setOverride(row.issueId, {fields: {...row.fields, [field.key]: previous}});
    },
    () => setIssueFields(props.store.state.config, row.issueId, {
      repo: props.store.state.config.repoFullName, values: {[field.key]: value},
    }),
  );
}

// onEstimateChange sets no optimistic guess: the server parses whatever format was typed and
// answers the true seconds, applied as the override once the write resolves.
function onEstimateChange(row: Row, event: Event) {
  const value = (event.target as HTMLInputElement).value;
  props.store.applyOptimistic(
    () => {},
    async () => {
      const facets = await setIssueEstimate(props.store.state.config, row.issueId, {
        repo: props.store.state.config.repoFullName, time_estimate: value,
      });
      props.store.setOverride(row.issueId, {timeEstimate: facets.time_estimate});
    },
  );
}
</script>

<template>
  <div class="planning-table tw-overflow-x-auto">
    <table class="ui very basic table">
      <thead>
        <tr>
          <th class="tw-cursor-pointer" @click="onSort('title')">Title</th>
          <th class="tw-cursor-pointer" @click="onSort('column')">Column</th>
          <th class="tw-cursor-pointer" @click="onSort('type')">Type</th>
          <th class="tw-cursor-pointer" @click="onSort('parent')">Parent</th>
          <th class="tw-cursor-pointer" @click="onSort('assignees')">Assignees</th>
          <th class="tw-cursor-pointer" @click="onSort('milestone')">Milestone</th>
          <th class="tw-cursor-pointer" @click="onSort('start')">Start</th>
          <th class="tw-cursor-pointer" @click="onSort('end')">End</th>
          <th class="tw-cursor-pointer" @click="onSort('estimate')">Estimate</th>
          <th class="tw-cursor-pointer" @click="onSort('tracked')">Tracked</th>
          <th class="tw-cursor-pointer" @click="onSort('points')">Points</th>
          <th v-for="field in fields" :key="field.key" class="tw-cursor-pointer" @click="onSort(field.key)">
            {{ field.label }}
          </th>
        </tr>
      </thead>
      <tbody>
        <template v-for="group in groupedRows" :key="group.key">
          <tr v-if="props.groupBy !== 'none'" class="planning-table-group">
            <td :colspan="columnCount"><strong>{{ group.label }}</strong> ({{ group.rows.length }})</td>
          </tr>
          <tr v-for="row in group.rows" :key="row.issueId">
            <td class="tw-flex tw-items-center tw-gap-1" :style="{paddingLeft: `${row.depth * 1.25}rem`}">
              <span
                v-if="isExpandable(row)" class="tw-cursor-pointer"
                role="button" :aria-label="isCollapsed(row) ? 'Expand' : 'Collapse'"
                @click="toggleCollapse(row)"
              ><svg-icon :name="isCollapsed(row) ? 'octicon-chevron-right' : 'octicon-chevron-down'"/></span>
              <svg-icon v-if="row.typeIcon" :name="typeIconName(row)!"/>
              <a :href="row.url">{{ row.title }} <span class="tw-text-text-light">#{{ row.number }}</span></a>
            </td>
            <td>{{ columnTitle(row.columnId) }}</td>
            <td>
              <select :value="row.typeId ?? 0" :disabled="!canEditIssues" @change="onTypeChange(row, $event)">
                <option :value="0">—</option>
                <option v-for="type in types" :key="type.id" :value="type.id">{{ type.name }}</option>
              </select>
            </td>
            <td>{{ parentLabel(row) }}</td>
            <td>{{ row.assignees.join(', ') }}</td>
            <td>
              <select :value="row.milestoneId ?? 0" :disabled="!canEditIssues" @change="onMilestoneChange(row, $event)">
                <option :value="0">No milestone</option>
                <option v-for="m in milestones" :key="m.milestone_id" :value="m.milestone_id">{{ m.title }}</option>
              </select>
            </td>
            <td>
              <input
                type="date" :value="unixToDateInput(row.startUnix)" :disabled="!canEditIssues"
                @change="onDateChange(row, 'start', $event)"
              >
            </td>
            <td>
              <input
                type="date" :value="unixToDateInput(row.endUnix)" :disabled="!canEditIssues"
                @change="onDateChange(row, 'end', $event)"
              >
            </td>
            <td>
              <input
                type="text" :value="estimateInputValue(row.timeEstimate)" :disabled="!canEditIssues"
                placeholder="1d 4h" @change="onEstimateChange(row, $event)"
              >
            </td>
            <td>{{ formatHours(row.trackedSeconds) }}</td>
            <td>{{ row.points }}</td>
            <td v-for="field in fields" :key="field.key">
              <input
                type="text" :value="row.fields[field.key] ?? ''" :disabled="!canEditIssues"
                @change="onFieldChange(row, field, $event)"
              >
            </td>
          </tr>
        </template>
      </tbody>
    </table>
  </div>
</template>
