// The roadmap's own client-side mirror of services/planning/hierarchy.go's RankAllows: a
// parent's rank must be numerically lower than its child's, so a lower number outranks a higher one.

import type {IssueType} from './types.ts';

export function allowedChildTypes(types: IssueType[], parentRank: number): IssueType[] {
  return types.filter((t) => t.rank > parentRank);
}

// defaultChildType picks the lowest-rank type a parent of parentRank allows as its child — the
// type immediately below the parent in the hierarchy — or undefined when none is allowed.
export function defaultChildType(types: IssueType[], parentRank: number): IssueType | undefined {
  let best: IssueType | undefined;
  for (const t of allowedChildTypes(types, parentRank)) {
    if (!best || t.rank < best.rank) best = t;
  }
  return best;
}
