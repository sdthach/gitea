// Dependency arrows over the roadmap's bars: pure geometry, no DOM. An arrow's endpoints are
// resolved against whatever the caller drew at each end — a bar's own rect, keyed by issue id,
// or a rollup bracket's rect, keyed by its rollup key (kind:key) — so the same helper draws
// both an issue-zoom arrow and one re-keyed onto a bracket.

export type ArrowLike = {
  from_issue_id: number;
  to_issue_id: number;
  enforced: boolean;
  from_rollup?: string;
  to_rollup?: string;
};

export type ArrowRect = {x: number; y: number; width: number; height: number};

export type ArrowPath = {
  key: string;
  fromKey: string;
  toKey: string;
  enforced: boolean;
  path: string;
};

// geometryKeyFor is how an arrow's own end names the rect to look up: the rollup key when the
// edge was re-keyed onto a bracket, the issue id otherwise.
export function geometryKeyFor(issueId: number, rollupKey?: string): string {
  return rollupKey || String(issueId);
}

// elbowPath draws an orthogonal path from the predecessor's right-middle edge to the
// successor's left-middle edge: a straight line when the two already sit on one row, otherwise
// an elbow that steps out from the predecessor, across, and into the successor — never a
// diagonal, which is what would read as "this line means nothing precise".
export function elbowPath(from: ArrowRect, to: ArrowRect): string {
  const startX = from.x + from.width;
  const startY = from.y + from.height / 2;
  const endX = to.x;
  const endY = to.y + to.height / 2;
  if (startY === endY) return `M ${startX} ${startY} L ${endX} ${endY}`;
  // The successor sits to the right: the elbow's vertical run is halfway across the gap. One
  // to its left (a backward edge) still needs somewhere to step out to, so it steps out a fixed
  // handle's width instead of a negative half-gap.
  const midX = endX >= startX ? startX + (endX - startX) / 2 : startX + 8;
  return `M ${startX} ${startY} L ${midX} ${startY} L ${midX} ${endY} L ${endX} ${endY}`;
}

// arrowPaths resolves every arrow whose both ends are on screen (geometry carries a rect for
// them) into the path it draws. An arrow with either end missing is dropped: a path to nothing
// drawn would point at empty space.
export function arrowPaths(arrows: ArrowLike[], geometry: Map<string, ArrowRect>): ArrowPath[] {
  const out: ArrowPath[] = [];
  for (const arrow of arrows) {
    const fromKey = geometryKeyFor(arrow.from_issue_id, arrow.from_rollup);
    const toKey = geometryKeyFor(arrow.to_issue_id, arrow.to_rollup);
    const from = geometry.get(fromKey);
    const to = geometry.get(toKey);
    if (!from || !to) continue;
    out.push({key: `${fromKey}>${toKey}`, fromKey, toKey, enforced: arrow.enforced, path: elbowPath(from, to)});
  }
  return out;
}
