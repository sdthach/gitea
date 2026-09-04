// The roadmap bar's pointer-drag state machine: pure, no DOM or store, built from a bar and a pointer position.

import {PX_PER_DAY, unixAtX, xOf} from './scale.ts';
import type {Scale} from './scale.ts';

export type DragKind = 'move' | 'resize-start' | 'resize-end' | 'row';
export type RowKind = 'assignee' | 'parent' | 'milestone';

export type DragBar = {
  issueId: number;
  startUnix: number;
  endUnix: number;
  rowKey: string;
};

export type RowLane = {key: string; kind: RowKind; top: number};

// rows lists every lane top to bottom, each rowHeight tall, in screen pixels relative to the
// same origin pointerY is measured from.
export type RowGeometry = {rowHeight: number; rows: RowLane[]};

export type DragContext = {
  origin: number;
  scale: Scale;
  rowGeometry: RowGeometry;
};

export type DragProposal = {start: number; end: number; row: string};

export type DragWrite =
  {kind: 'dates'; issueId: number; start: string; end: string} |
  {kind: 'group'; issueId: number; groupBy: 'assignee'; group: string} |
  {kind: 'parent'; issueId: number; parentIssueId: number} |
  {kind: 'milestone'; issueId: number; milestoneId: number};

export type Drag = {
  update(x: number, y: number): DragProposal;
  cancel(): void;
  commit(): DragWrite[];
};

const DAY_SECONDS = 86400;

function shiftByDays(unix: number, days: number, origin: number, scale: Scale): number {
  return unixAtX(xOf(unix, origin, scale) + days * PX_PER_DAY[scale], origin, scale);
}

// rowAt finds the lane whose [top, top + rowHeight) band contains y, clamped to the first or
// last lane when y falls outside every band — a drag that overshoots the header or the bottom
// row still resolves to a real row rather than none.
export function rowAt(geometry: RowGeometry, y: number): RowLane | undefined {
  const {rows, rowHeight} = geometry;
  if (rows.length === 0) return undefined;
  for (const row of rows) {
    if (y >= row.top && y < row.top + rowHeight) return row;
  }
  return y < rows[0].top ? rows[0] : rows[rows.length - 1];
}

export function isoDate(unix: number): string {
  return new Date(unix * 1000).toISOString().slice(0, 10);
}

// targetRowIndex resolves Alt+Up/Down to the row immediately above or below a bar's own row,
// addressed by index rather than key: two bars sharing a lane key still resolve to their own
// neighboring row instead of both jumping relative to the first row that key appears on.
export function targetRowIndex(index: number, direction: 'up' | 'down', total: number): number | null {
  const target = direction === 'up' ? index - 1 : index + 1;
  return target >= 0 && target < total ? target : null;
}

// _pointerY rounds out begin's own signature; row geometry is in absolute coordinates, so only
// update's own y (never this one) ever resolves a row.
export function begin(kind: DragKind, bar: DragBar, pointerX: number, _pointerY: number, ctx: DragContext): Drag {
  let cancelled = false;
  let proposedStart = bar.startUnix;
  let proposedEnd = bar.endUnix;
  let proposedRow = bar.rowKey;

  function update(x: number, y: number): DragProposal {
    if (!cancelled) {
      const dxDays = Math.round((x - pointerX) / PX_PER_DAY[ctx.scale]);
      switch (kind) {
        case 'move': {
          const duration = bar.endUnix - bar.startUnix;
          proposedStart = shiftByDays(bar.startUnix, dxDays, ctx.origin, ctx.scale);
          proposedEnd = proposedStart + duration;
          proposedRow = rowAt(ctx.rowGeometry, y)?.key ?? proposedRow;
          break;
        }
        case 'resize-start': {
          const latestStart = bar.endUnix - DAY_SECONDS;
          proposedStart = Math.min(shiftByDays(bar.startUnix, dxDays, ctx.origin, ctx.scale), latestStart);
          break;
        }
        case 'resize-end': {
          const earliestEnd = bar.startUnix + DAY_SECONDS;
          proposedEnd = Math.max(shiftByDays(bar.endUnix, dxDays, ctx.origin, ctx.scale), earliestEnd);
          break;
        }
        case 'row':
          proposedRow = rowAt(ctx.rowGeometry, y)?.key ?? proposedRow;
          break;
      }
    }
    return {start: proposedStart, end: proposedEnd, row: proposedRow};
  }

  function cancel(): void {
    cancelled = true;
    proposedStart = bar.startUnix;
    proposedEnd = bar.endUnix;
    proposedRow = bar.rowKey;
  }

  function commit(): DragWrite[] {
    const writes: DragWrite[] = [];
    if (proposedStart !== bar.startUnix || proposedEnd !== bar.endUnix) {
      writes.push({kind: 'dates', issueId: bar.issueId, start: isoDate(proposedStart), end: isoDate(proposedEnd)});
    }
    if (proposedRow !== bar.rowKey) {
      const rowKind = ctx.rowGeometry.rows.find((r) => r.key === proposedRow)?.kind ??
        ctx.rowGeometry.rows.find((r) => r.key === bar.rowKey)?.kind ??
        'assignee';
      if (rowKind === 'assignee') writes.push({kind: 'group', issueId: bar.issueId, groupBy: 'assignee', group: proposedRow});
      else if (rowKind === 'parent') writes.push({kind: 'parent', issueId: bar.issueId, parentIssueId: Number(proposedRow)});
      else writes.push({kind: 'milestone', issueId: bar.issueId, milestoneId: Number(proposedRow)});
    }
    return writes;
  }

  return {update, cancel, commit};
}
