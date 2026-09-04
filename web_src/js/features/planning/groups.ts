// Mirrors services/planning/board.go's GroupKeyFor and BuildGroups ordering, so a client-side
// group-by on the table view lands rows in the same groups, in the same order, as the server.

export type Grouping = 'type' | 'assignee' | 'parent' | 'none';

export type GroupInput = {
  typeName?: string;
  assignees?: string[];
  rootIssueId?: number;
  hasChildren?: boolean;
};

export function groupKey(row: GroupInput, groupBy: Grouping): string {
  switch (groupBy) {
    case 'type':
      return (row.typeName ?? '').trim();
    case 'parent':
      if (!row.hasChildren) return '';
      return String(row.rootIssueId ?? 0);
    case 'assignee': {
      const assignees = row.assignees ?? [];
      if (assignees.length === 0) return '';
      return [...assignees].sort()[0];
    }
    default:
      return '';
  }
}

// orderGroupKeys sorts the empty-value group last and every other key lexicographically,
// the same rule BuildGroups applies to its own group order.
export function orderGroupKeys(keys: Iterable<string>): string[] {
  const unique = [...new Set(keys)];
  unique.sort((a, b) => {
    if ((a === '') !== (b === '')) return a === '' ? 1 : -1;
    if (a < b) return -1;
    if (a > b) return 1;
    return 0;
  });
  return unique;
}

export function emptyGroupLabel(groupBy: Grouping): string {
  switch (groupBy) {
    case 'type': return 'no type assigned';
    case 'assignee': return 'unassigned';
    case 'parent': return 'no parent';
    default: return 'All issues';
  }
}
