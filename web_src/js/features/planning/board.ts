// The board's own drop math: which cards a drop's writes touch and what the destination
// column looks like afterward. Kept pure so a drop's write plan is a plain function of the
// board and where a card landed, testable with no DOM and no store.

import {groupKey} from './groups.ts';
import type {Grouping} from './groups.ts';
import type {Board, Card, GroupColumn} from './types.ts';

export type BoardWrite =
  {kind: 'group'; issueId: number; groupBy: Grouping; group: string} |
  {kind: 'order'; columnId: number; issueIds: number[]};

export type DropTarget = {
  columnId: number;
  groupKeyValue: string;
  // Every card the destination group x column cell holds after the drop, in their new visual
  // order, the moved card included.
  cellIssueIds: number[];
};

// cardGroupInput mirrors TableView's own groupHasChildren: a card groups under its root issue
// once it has children of its own, or is itself one of another card's children.
export function cardGroupInput(card: Card) {
  return {
    typeName: card.type,
    assignees: card.assignees,
    rootIssueId: card.root_issue_id,
    hasChildren: Boolean(card.has_children) || (Boolean(card.root_issue_id) && card.root_issue_id !== card.issue_id),
  };
}

function findCard(board: Board, issueId: number): Card | undefined {
  for (const group of board.groups) {
    for (const column of group.columns) {
      const card = column.cards.find((c) => c.issue_id === issueId);
      if (card) return card;
    }
  }
  return undefined;
}

export function findColumn(board: Board, columnId: number): GroupColumn | undefined {
  for (const group of board.groups) {
    const column = group.columns.find((c) => c.column_id === columnId);
    if (column) return column;
  }
  return undefined;
}

// columnCardsAcrossGroups gathers every card the server holds under columnId, across every
// group's own copy of that column, in each group's original relative order. The server hands
// each group its own full copy of every column rather than splitting cards between them, so an
// order write naming only the drop's own group would silently drop every other group's cards
// from the column.
function columnCardsAcrossGroups(board: Board, columnId: number): Card[] {
  const cards: Card[] = [];
  for (const group of board.groups) {
    const column = group.columns.find((c) => c.column_id === columnId);
    if (column) cards.push(...column.cards);
  }
  return cards;
}

// mergeGroupIntoColumn replaces the destination cell's own cards, wherever they sit in the
// column's existing order, with cellIssueIds: every other group's card keeps its exact slot,
// so reordering one swimlane never reshuffles another's within the same column.
function mergeGroupIntoColumn(otherCards: Card[], groupBy: Grouping, groupKeyValue: string, cellIssueIds: number[]): number[] {
  const result: number[] = [];
  let inserted = false;
  for (const card of otherCards) {
    if (groupKey(cardGroupInput(card), groupBy) === groupKeyValue) {
      if (!inserted) {
        result.push(...cellIssueIds);
        inserted = true;
      }
      continue;
    }
    result.push(card.issue_id);
  }
  if (!inserted) result.push(...cellIssueIds);
  return result;
}

// planDrop is the write plan a board drop issues: a group write only when the drop changes the
// active grouping's value, followed by an order write naming every card the destination column
// ends up with — but only when a drop back onto its own slot would actually change that order.
// MoveIssuesOnProjectColumn (services/projects/issue.go) assigns a card's column as part of
// sorting it, so a drop across columns needs no separate column write — the order write alone
// moves the card in.
export function planDrop(board: Board, groupBy: Grouping, issueId: number, target: DropTarget): BoardWrite[] {
  const writes: BoardWrite[] = [];
  let groupChanged = false;
  if (groupBy !== 'none') {
    const moved = findCard(board, issueId);
    const fromGroup = moved ? groupKey(cardGroupInput(moved), groupBy) : undefined;
    groupChanged = fromGroup !== target.groupKeyValue;
    if (groupChanged) {
      writes.push({kind: 'group', issueId, groupBy, group: target.groupKeyValue});
    }
  }
  const columnCards = columnCardsAcrossGroups(board, target.columnId);
  const otherCards = columnCards.filter((c) => c.issue_id !== issueId);
  const issueIds = mergeGroupIntoColumn(otherCards, groupBy, target.groupKeyValue, target.cellIssueIds);
  const currentOrder = columnCards.map((c) => c.issue_id);
  const orderChanged = issueIds.length !== currentOrder.length || issueIds.some((id, i) => id !== currentOrder[i]);
  // groupChanged still needs the order write even when the numeric order coincides with the
  // old one: the card left its old cell, so the column's own view of who belongs to which
  // group changed even where the raw id sequence did not.
  if (groupChanged || orderChanged) writes.push({kind: 'order', columnId: target.columnId, issueIds});
  return writes;
}

// mergeVisibleOrderIntoCell rebuilds one cell's full card order after a drop that only reordered
// the cards a search filter currently shows: a card the filter is hiding keeps its earlier slot
// among the other still-hidden ones, in the same block-replace shape as mergeGroupIntoColumn,
// rather than being dropped from the column write — and so from the column itself — because the
// filtered view never showed it to be reordered.
export function mergeVisibleOrderIntoCell(cellCardsBeforeDrop: Card[], visibleOrderedIds: number[]): number[] {
  const visible = new Set(visibleOrderedIds);
  const result: number[] = [];
  let inserted = false;
  for (const card of cellCardsBeforeDrop) {
    if (visible.has(card.issue_id)) {
      if (!inserted) {
        result.push(...visibleOrderedIds);
        inserted = true;
      }
      continue;
    }
    result.push(card.issue_id);
  }
  if (!inserted) result.push(...visibleOrderedIds);
  return result;
}

// applyCardGroupFields sets the fields a group write would end up assigning, so the optimistic
// card carries them before the server confirms. An empty groupKeyValue clears the field, same
// as the endpoint itself.
function applyCardGroupFields(card: Card, groupBy: Grouping, groupKeyValue: string, types: Board['types']): void {
  switch (groupBy) {
    case 'type': {
      const type = types.find((t) => t.name === groupKeyValue);
      card.type = type?.name;
      card.type_id = type?.id;
      card.type_color = type?.color;
      card.type_icon = type?.icon;
      return;
    }
    case 'assignee':
      card.assignees = groupKeyValue ? [groupKeyValue] : [];
      return;
    case 'parent': {
      const rootId = groupKeyValue ? Number(groupKeyValue) : undefined;
      card.parent_issue_id = rootId;
      card.root_issue_id = rootId;
      card.has_children = false;
      break;
    }
    default:
  }
}

// applyDrop mutates board in place to the drop's optimistic result — writes is planDrop's own
// output, so the destination column ends up in exactly the order the write asks the server for
// — and returns a revert closure undoing exactly that mutation, or undefined when the card or
// the destination column cannot be found (nothing was changed).
export function applyDrop(board: Board, groupBy: Grouping, issueId: number, target: DropTarget, writes: BoardWrite[]): (() => void) | undefined {
  const card = findCard(board, issueId);
  const destColumn = findColumn(board, target.columnId);
  if (!card || !destColumn) return undefined;

  let sourceColumn: GroupColumn | undefined;
  let sourceIndex = -1;
  for (const group of board.groups) {
    for (const column of group.columns) {
      const index = column.cards.indexOf(card);
      if (index !== -1) {
        sourceColumn = column;
        sourceIndex = index;
      }
    }
  }
  if (!sourceColumn) return undefined;

  const prevCard = {...card};
  const prevSourceCards = [...sourceColumn.cards];
  const prevDestCards = destColumn === sourceColumn ? prevSourceCards : [...destColumn.cards];

  sourceColumn.cards.splice(sourceIndex, 1);
  if (groupBy !== 'none') applyCardGroupFields(card, groupBy, target.groupKeyValue, board.types);
  card.column_id = target.columnId;

  const order = writes.find((w): w is Extract<BoardWrite, {kind: 'order'}> => w.kind === 'order');
  const byId = new Map(destColumn.cards.filter((c) => c.issue_id !== issueId).map((c) => [c.issue_id, c] as const));
  byId.set(issueId, card);
  destColumn.cards = (order?.issueIds ?? [issueId]).map((id) => byId.get(id)).filter((c): c is Card => Boolean(c));
  for (const [i, c] of destColumn.cards.entries()) c.sorting = i;

  const capturedSource = sourceColumn;
  return () => {
    Object.assign(card, prevCard);
    capturedSource.cards = prevSourceCards;
    if (destColumn !== capturedSource) destColumn.cards = prevDestCards;
  };
}
