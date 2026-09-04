<script lang="ts" setup>
import {computed, nextTick, onMounted, onUnmounted, reactive, watch} from 'vue';
import type {ComponentPublicInstance} from 'vue';
import type {SortableEvent} from 'sortablejs';
import type SortableType from 'sortablejs';
import {createSortable} from '../../../modules/sortable.ts';
import {applyDrop, cardGroupInput, findColumn, mergeVisibleOrderIntoCell, planDrop} from '../board.ts';
import type {BoardWrite} from '../board.ts';
import {emptyGroupLabel, groupKey, orderGroupKeys} from '../groups.ts';
import type {Grouping} from '../groups.ts';
import {addCard, moveIssueColumn, moveIssueGroup, orderColumn} from '../api.ts';
import {filterRows} from '../filter.ts';
import type {FieldKind, FilterRow} from '../filter.ts';
import type {PlanningStore} from '../store.ts';
import type {Board, Card} from '../types.ts';
import BoardCard from './BoardCard.vue';

const props = defineProps<{store: PlanningStore; groupBy: Grouping; query: string; canEditIssues: boolean}>();

const board = computed<Board | null>(() => props.store.state.board);

const cards = computed<Card[]>(() => {
  const b = board.value;
  if (!b) return [];
  const byId = new Map<number, Card>();
  for (const group of b.groups) {
    for (const column of group.columns) {
      for (const card of column.cards) byId.set(card.issue_id, card);
    }
  }
  return [...byId.values()];
});

const fieldKinds = computed<Record<string, FieldKind>>(
  () => Object.fromEntries((board.value?.fields ?? []).map((field) => [field.key, field.kind as FieldKind])),
);

// cardFilterRow adapts a card to the table's own filter grammar (filter.ts), so a search on
// the board narrows on the same key:value clauses, not a name-only substring match.
function cardFilterRow(card: Card): FilterRow & {card: Card} {
  return {
    card,
    title: card.title,
    isClosed: card.is_closed,
    type: card.type,
    assignees: card.assignees,
    labels: card.labels,
    milestone: card.milestone,
    parentIssueId: card.parent_issue_id,
    fields: card.fields,
  };
}

const filteredCards = computed(() => filterRows(cards.value.map(cardFilterRow), props.query, fieldKinds.value).map((row) => row.card));

type BoardGroup = {key: string; label: string; cards: Card[]};

const groups = computed<BoardGroup[]>(() => {
  const keyed = filteredCards.value.map((card) => ({card, key: groupKey(cardGroupInput(card), props.groupBy)}));
  const rootTitle = (id: string) => cards.value.find((c) => c.issue_id === Number(id))?.title ?? id;
  return orderGroupKeys(keyed.map((entry) => entry.key)).map((key) => ({
    key,
    label: key === '' ? emptyGroupLabel(props.groupBy) : (props.groupBy === 'parent' ? rootTitle(key) : key),
    cards: keyed.filter((entry) => entry.key === key).map((entry) => entry.card),
  }));
});

function cellCards(groupKeyValue: string, columnId: number): Card[] {
  const group = groups.value.find((g) => g.key === groupKeyValue);
  return (group?.cards ?? [])
    .filter((c) => c.column_id === columnId)
    .sort((a, b) => a.sorting - b.sorting);
}

// fullCellCards is cellCards' unfiltered counterpart, read straight from the board's own data
// rather than the search-filtered groups computed above: a drop write has to keep every card the
// filter is hiding, not just the ones on screen.
function fullCellCards(groupKeyValue: string, columnId: number): Card[] {
  const group = board.value?.groups.find((g) => g.key === groupKeyValue);
  const column = group?.columns.find((c) => c.column_id === columnId);
  return (column?.cards ?? []).slice().sort((a, b) => a.sorting - b.sorting);
}

function columnCount(columnId: number): number {
  return filteredCards.value.filter((c) => c.column_id === columnId).length;
}

const addingTo = reactive<{columnId: number | null; groupKeyValue: string; title: string; typeId: number}>({
  columnId: null, groupKeyValue: '', title: '', typeId: 0,
});

function startAdd(columnId: number, groupKeyValue: string) {
  addingTo.columnId = columnId;
  addingTo.groupKeyValue = groupKeyValue;
  addingTo.title = '';
  addingTo.typeId = 0;
}

function cancelAdd() {
  addingTo.columnId = null;
}

async function submitAdd() {
  const title = addingTo.title.trim();
  if (!title || addingTo.columnId === null) return;
  const config = props.store.state.config;
  const groupBy = props.groupBy === 'none' ? undefined : props.groupBy;
  const needsType = props.groupBy === 'parent' && addingTo.groupKeyValue !== '';
  if (needsType && !addingTo.typeId) return;
  const result = await addCard(config, {
    repo: config.repoFullName, project_id: config.projectId, column_id: addingTo.columnId, title,
    group_by: groupBy, group: groupBy ? addingTo.groupKeyValue : undefined,
    type_id: needsType ? addingTo.typeId : undefined,
  });
  props.store.setBoard(result);
  cancelAdd();
}

// cellRefs/sortables key a Sortable instance to one group x column cell, so a board with many
// groups and columns gets exactly one drag surface per cell rather than one per column.
const cellRefs = new Map<string, HTMLElement>();
const sortables = new Map<string, SortableType>();

function cellKey(groupKeyValue: string, columnId: number): string {
  return `${groupKeyValue}::${columnId}`;
}

function setCellRef(groupKeyValue: string, columnId: number, el: Element | ComponentPublicInstance | null) {
  const key = cellKey(groupKeyValue, columnId);
  if (el instanceof HTMLElement) cellRefs.set(key, el);
  else cellRefs.delete(key);
}

async function issueWrite(write: BoardWrite): Promise<Board> {
  const config = props.store.state.config;
  const projectId = config.projectId;
  const repo = config.repoFullName;
  if (write.kind === 'group') {
    return moveIssueGroup(config, write.issueId, {repo, project_id: projectId, group_by: write.groupBy, group: write.group});
  }
  return orderColumn(config, write.columnId, {repo, project_id: projectId, issue_ids: write.issueIds, group_by: props.groupBy === 'none' ? undefined : props.groupBy});
}

// handleDrop undoes sortablejs's own DOM move first — Vue owns this DOM, driven by board's
// reactive arrays — then applies the drop optimistically to the store and issues its writes.
function handleDrop(evt: SortableEvent) {
  const {item, from, to, oldIndex, newIndex} = evt;
  if (oldIndex === undefined || newIndex === undefined || !board.value) return;
  if (from === to && oldIndex === newIndex) return; // dropped back onto its own slot
  const issueId = Number(item.dataset.issueId);
  const toColumnId = Number((to as HTMLElement).dataset.columnId);
  const toGroupKey = (to as HTMLElement).dataset.groupKey ?? '';

  to.removeChild(item);
  from.insertBefore(item, from.children[oldIndex] ?? null);

  const visibleIds = cellCards(toGroupKey, toColumnId).map((c) => c.issue_id).filter((id) => id !== issueId);
  visibleIds.splice(newIndex, 0, issueId);
  const destCellIds = mergeVisibleOrderIntoCell(fullCellCards(toGroupKey, toColumnId), visibleIds);

  const writes = planDrop(board.value, props.groupBy, issueId, {columnId: toColumnId, groupKeyValue: toGroupKey, cellIssueIds: destCellIds});
  if (writes.length === 0) return;
  props.store.applyOptimistic(
    () => applyDrop(board.value!, props.groupBy, issueId, {columnId: toColumnId, groupKeyValue: toGroupKey, cellIssueIds: destCellIds}, writes),
    async () => {
      let result: Board | undefined;
      for (const write of writes) result = await issueWrite(write);
      if (result) props.store.setBoard(result);
    },
  );
}

async function initSortables() {
  for (const [key, sortable] of sortables) {
    if (!props.canEditIssues || !cellRefs.has(key)) {
      sortable.destroy();
      sortables.delete(key);
    }
  }
  if (!props.canEditIssues) return;
  for (const [key, el] of cellRefs) {
    if (sortables.has(key)) continue;
    const sortable = await createSortable(el, {group: 'planning-board', delay: 300, delayOnTouchOnly: true, draggable: '.board-card', onEnd: handleDrop});
    sortables.set(key, sortable);
  }
}

watch(() => [groups.value, props.canEditIssues], () => { void nextTick(initSortables); }, {flush: 'post'});
onMounted(() => { void nextTick(initSortables); });
onUnmounted(() => {
  for (const sortable of sortables.values()) sortable.destroy();
  sortables.clear();
});

// moveByKeyboard mirrors a one-slot drag: Up/Down swap the card with its neighbour in the same
// column, Left/Right hand it to the adjacent column, appended, through the lighter single-card
// endpoint since there is no drop position to preserve order around.
function moveByKeyboard(card: Card, direction: 'up' | 'down' | 'left' | 'right') {
  if (!props.canEditIssues || !board.value) return;
  const groupKeyValue = groupKey(cardGroupInput(card), props.groupBy);
  if (direction === 'up' || direction === 'down') {
    const siblings = cellCards(groupKeyValue, card.column_id);
    const index = siblings.findIndex((c) => c.issue_id === card.issue_id);
    const swapWith = direction === 'up' ? index - 1 : index + 1;
    if (swapWith < 0 || swapWith >= siblings.length) return;
    const ids = siblings.map((c) => c.issue_id);
    [ids[index], ids[swapWith]] = [ids[swapWith], ids[index]];
    const cellIssueIds = mergeVisibleOrderIntoCell(fullCellCards(groupKeyValue, card.column_id), ids);
    const writes = planDrop(board.value, props.groupBy, card.issue_id, {columnId: card.column_id, groupKeyValue, cellIssueIds});
    if (writes.length === 0) return;
    props.store.applyOptimistic(
      () => applyDrop(board.value!, props.groupBy, card.issue_id, {columnId: card.column_id, groupKeyValue, cellIssueIds}, writes),
      async () => {
        let result: Board | undefined;
        for (const write of writes) result = await issueWrite(write);
        if (result) props.store.setBoard(result);
      },
    );
    return;
  }

  const columns = board.value.columns;
  const columnIndex = columns.findIndex((c) => c.column_id === card.column_id);
  const targetIndex = direction === 'left' ? columnIndex - 1 : columnIndex + 1;
  if (targetIndex < 0 || targetIndex >= columns.length) return;
  const targetColumnId = columns[targetIndex].column_id;
  const config = props.store.state.config;
  const previous = {columnId: card.column_id, sorting: card.sorting};
  props.store.applyOptimistic(
    () => {
      const column = findColumn(board.value!, targetColumnId);
      const source = findColumn(board.value!, previous.columnId);
      if (!column || !source) return undefined;
      const at = source.cards.indexOf(card);
      if (at !== -1) source.cards.splice(at, 1);
      card.column_id = targetColumnId;
      card.sorting = column.cards.length;
      column.cards.push(card);
      return () => {
        const dest = findColumn(board.value!, targetColumnId);
        const at2 = dest?.cards.indexOf(card) ?? -1;
        if (dest && at2 !== -1) dest.cards.splice(at2, 1);
        card.column_id = previous.columnId;
        card.sorting = previous.sorting;
        source.cards.push(card);
      };
    },
    () => moveIssueColumn(config, card.issue_id, {repo: config.repoFullName, project_id: config.projectId, column_id: targetColumnId}),
  );
}

function onCardKeydown(card: Card, event: KeyboardEvent) {
  if (!event.altKey) return;
  const key = {ArrowUp: 'up', ArrowDown: 'down', ArrowLeft: 'left', ArrowRight: 'right'}[event.key];
  if (!key) return;
  event.preventDefault();
  moveByKeyboard(card, key as 'up' | 'down' | 'left' | 'right');
}
</script>

<template>
  <div v-if="board" class="planning-board tw-overflow-x-auto">
    <div class="tw-flex tw-gap-2 tw-mb-2">
      <div v-for="column in board.columns" :key="column.column_id" class="tw-w-72 tw-flex-shrink-0 tw-flex tw-items-center tw-justify-between tw-font-semibold">
        <span>{{ column.title }}</span>
        <span class="tw-text-text-light">{{ columnCount(column.column_id) }}</span>
      </div>
    </div>

    <div v-for="group in groups" :key="group.key" class="tw-mb-4">
      <div v-if="groupBy !== 'none'" class="tw-font-semibold tw-mb-1">{{ group.label }} ({{ group.cards.length }})</div>
      <div class="tw-flex tw-gap-2 tw-items-start">
        <div
          v-for="column in board.columns" :key="column.column_id"
          :ref="(el) => setCellRef(group.key, column.column_id, el)"
          :data-column-id="column.column_id" :data-group-key="group.key"
          class="board-column-cell tw-w-72 tw-flex-shrink-0 tw-flex tw-flex-col tw-gap-2"
        >
          <BoardCard
            v-for="card in cellCards(group.key, column.column_id)" :key="card.issue_id"
            :card="card" :labels="board.labels" :can-edit-issues="canEditIssues"
            tabindex="0" @keydown="onCardKeydown(card, $event)"
          />

          <template v-if="canEditIssues">
            <form v-if="addingTo.columnId === column.column_id && addingTo.groupKeyValue === group.key" class="tw-flex tw-flex-col tw-gap-1" @submit.prevent="submitAdd">
              <input v-model="addingTo.title" class="tw-w-full" type="text" placeholder="Title" autofocus @keydown.escape="cancelAdd">
              <select v-if="groupBy === 'parent' && group.key !== ''" v-model.number="addingTo.typeId" class="ui dropdown">
                <option :value="0">Select a type…</option>
                <option v-for="type in board.types" :key="type.id" :value="type.id">{{ type.name }}</option>
              </select>
            </form>
            <button v-else type="button" class="tw-text-left tw-text-text-light" @click="startAdd(column.column_id, group.key)">+ Add item</button>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>
