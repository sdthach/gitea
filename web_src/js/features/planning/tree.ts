// Builds the hierarchy from the tree[] edges the board and roadmap both publish. Every walk
// tracks visited ids so a corrupt or malicious edge list (a cycle) terminates instead of
// hanging the tab.

export type TreeEdge = {
  issue_id: number;
  parent_issue_id: number;
};

const MAX_DEPTH = 64;

export function buildTree(edges: TreeEdge[]): Map<number, number> {
  const parentOf = new Map<number, number>();
  for (const edge of edges) parentOf.set(edge.issue_id, edge.parent_issue_id);
  return parentOf;
}

export function rootOf(parentOf: Map<number, number>, issueId: number): number {
  const seen = new Set<number>([issueId]);
  let current = issueId;
  for (let i = 0; i < MAX_DEPTH; i++) {
    const parent = parentOf.get(current);
    if (!parent || parent === current || seen.has(parent)) return current;
    seen.add(parent);
    current = parent;
  }
  return current;
}

export function depthOf(parentOf: Map<number, number>, issueId: number): number {
  const seen = new Set<number>([issueId]);
  let current = issueId;
  let depth = 0;
  for (let i = 0; i < MAX_DEPTH; i++) {
    const parent = parentOf.get(current);
    if (!parent || parent === current || seen.has(parent)) return depth;
    seen.add(parent);
    current = parent;
    depth++;
  }
  return depth;
}

// treeOrder arranges rows depth-first: a parent immediately precedes its own children, with
// every subtree kept together, so indentation reads as a tree rather than a flat list ordered
// by an unrelated column. Rows keep their incoming relative order among siblings and among
// top-level roots, so sorting first (by title, say) and then calling this ranks siblings by
// that sort while still nesting them under their parent. The cycle guard mirrors rootOf and
// depthOf: a row already placed on the current walk is not visited again.
export function treeOrder<T extends {issueId: number; parentIssueId?: number}>(rows: T[]): T[] {
  const ids = new Set(rows.map((row) => row.issueId));
  const childrenOf = new Map<number, T[]>();
  const roots: T[] = [];
  for (const row of rows) {
    const parent = row.parentIssueId;
    if (parent && parent !== row.issueId && ids.has(parent)) {
      if (!childrenOf.has(parent)) childrenOf.set(parent, []);
      childrenOf.get(parent)!.push(row);
    } else {
      roots.push(row);
    }
  }

  const out: T[] = [];
  const seen = new Set<number>();
  function walk(row: T) {
    if (seen.has(row.issueId)) return;
    seen.add(row.issueId);
    out.push(row);
    for (const child of childrenOf.get(row.issueId) ?? []) walk(child);
  }
  for (const root of roots) walk(root);
  // A pure cycle has no row without a parent inside the set, so the walk above never starts
  // one; every row still comes out exactly once, each still-unseen one arbitrarily rooting
  // its own cycle rather than being dropped.
  for (const row of rows) walk(row);
  return out;
}

// visibleRows drops a row whenever any ancestor between it and the root is collapsed. The
// cycle guard mirrors rootOf/depthOf: an ancestor already visited on this walk stops the
// walk rather than looping forever.
export function visibleRows<T extends {issueId: number; parentIssueId?: number}>(rows: T[], collapsed: Set<number>): T[] {
  const byId = new Map(rows.map((row) => [row.issueId, row]));

  function isHidden(row: T): boolean {
    const seen = new Set<number>([row.issueId]);
    let current = row.parentIssueId;
    while (current) {
      if (seen.has(current)) return false;
      if (collapsed.has(current)) return true;
      seen.add(current);
      current = byId.get(current)?.parentIssueId;
    }
    return false;
  }

  return rows.filter((row) => !isHidden(row));
}
