// The table's filter grammar: free words match the title case-insensitively, `key:value`
// clauses narrow on a fixed set of built-in fields, and any other key is looked up in
// fieldKinds — a custom field when found there, free text otherwise. Every clause in a query
// must match (AND); quoting a value (`key:"a b"`) keeps spaces out of the tokenizer's way.

export type FieldKind = 'int' | 'text' | 'date' | 'select';

export type FilterRow = {
  title: string;
  isClosed: boolean;
  type?: string;
  assignees?: string[];
  labels?: string[];
  milestone?: string;
  parentIssueId?: number;
  fields?: Record<string, unknown>;
};

type FieldOp = 'eq' | 'gt' | 'gte' | 'lt' | 'lte' | 'range';

type Clause =
  | {kind: 'free'; text: string} |
  {kind: 'is'; value: 'open' | 'closed'} |
  {kind: 'no'; field: 'type' | 'assignee' | 'milestone' | 'parent'} |
  {kind: 'parent'; value: 'none' | number} |
  {kind: 'type' | 'assignee' | 'label' | 'milestone'; value: string} |
  {kind: 'field'; key: string; op: FieldOp; value: string; value2?: string};

// tokenize splits on whitespace, except inside a double-quoted span (which may sit right
// after a `key:`), and strips the quotes from the resulting token.
export function tokenize(query: string): string[] {
  const tokens: string[] = [];
  let i = 0;
  const n = query.length;
  while (i < n) {
    while (i < n && /\s/.test(query[i])) i++;
    if (i >= n) break;
    let token = '';
    while (i < n && !/\s/.test(query[i])) {
      if (query[i] === '"') {
        i++;
        while (i < n && query[i] !== '"') {
          token += query[i];
          i++;
        }
      } else {
        token += query[i];
      }
      i++;
    }
    tokens.push(token);
  }
  return tokens;
}

function parseFieldValue(key: string, raw: string): Clause {
  if (raw.includes('..')) {
    const idx = raw.indexOf('..');
    return {kind: 'field', key, op: 'range', value: raw.slice(0, idx), value2: raw.slice(idx + 2)};
  }
  if (raw.startsWith('>=')) return {kind: 'field', key, op: 'gte', value: raw.slice(2)};
  if (raw.startsWith('<=')) return {kind: 'field', key, op: 'lte', value: raw.slice(2)};
  if (raw.startsWith('>')) return {kind: 'field', key, op: 'gt', value: raw.slice(1)};
  if (raw.startsWith('<')) return {kind: 'field', key, op: 'lt', value: raw.slice(1)};
  return {kind: 'field', key, op: 'eq', value: raw};
}

const NO_FIELDS = new Set(['type', 'assignee', 'milestone', 'parent']);

function parseToken(token: string, fieldKinds: Record<string, FieldKind>): Clause {
  const idx = token.indexOf(':');
  if (idx <= 0) return {kind: 'free', text: token};
  const key = token.slice(0, idx).toLowerCase();
  const value = token.slice(idx + 1);

  switch (key) {
    case 'is':
      if (value === 'open' || value === 'closed') return {kind: 'is', value};
      return {kind: 'free', text: token};
    case 'no':
      if (NO_FIELDS.has(value)) return {kind: 'no', field: value as 'type' | 'assignee' | 'milestone' | 'parent'};
      return {kind: 'free', text: token};
    case 'parent': {
      if (value === 'none') return {kind: 'parent', value: 'none'};
      const m = /^#?(\d+)$/.exec(value);
      if (m) return {kind: 'parent', value: Number(m[1])};
      return {kind: 'free', text: token};
    }
    case 'type':
    case 'assignee':
    case 'label':
    case 'milestone':
      return {kind: key, value};
    default:
      if (key in fieldKinds) return parseFieldValue(key, value);
      return {kind: 'free', text: token};
  }
}

function compare(actual: number, op: FieldOp, value: number, value2?: number): boolean {
  switch (op) {
    case 'eq': return actual === value;
    case 'gt': return actual > value;
    case 'gte': return actual >= value;
    case 'lt': return actual < value;
    case 'lte': return actual <= value;
    case 'range': return actual >= value && actual <= (value2 ?? value);
  }
}

function matchField(row: FilterRow, clause: {key: string; op: FieldOp; value: string; value2?: string}, kind: FieldKind): boolean {
  const actual = row.fields?.[clause.key] as string | number | undefined | null;
  if (actual === undefined || actual === null || actual === '') return false;

  if (kind === 'int') {
    return compare(Number(actual), clause.op, Number(clause.value), clause.value2 === undefined ? undefined : Number(clause.value2));
  }
  if (kind === 'date') {
    // Dates are YYYY-MM-DD; that format sorts lexicographically the same as chronologically.
    const actualStr = String(actual).slice(0, 10);
    switch (clause.op) {
      case 'eq': return actualStr === clause.value;
      case 'gt': return actualStr > clause.value;
      case 'gte': return actualStr >= clause.value;
      case 'lt': return actualStr < clause.value;
      case 'lte': return actualStr <= clause.value;
      case 'range': return actualStr >= clause.value && actualStr <= (clause.value2 ?? clause.value);
    }
  }
  // text and select: exact match only, case-insensitive.
  return String(actual).toLowerCase() === clause.value.toLowerCase();
}

function matchClause(row: FilterRow, clause: Clause, fieldKinds: Record<string, FieldKind>): boolean {
  switch (clause.kind) {
    case 'free':
      return row.title.toLowerCase().includes(clause.text.toLowerCase());
    case 'is':
      return clause.value === 'closed' ? row.isClosed : !row.isClosed;
    case 'no':
      return {
        type: !row.type,
        assignee: !row.assignees?.length,
        milestone: !row.milestone,
        parent: !row.parentIssueId,
      }[clause.field];
    case 'parent':
      if (clause.value === 'none') return !row.parentIssueId;
      return row.parentIssueId === clause.value;
    case 'type':
      return (row.type ?? '').toLowerCase() === clause.value.toLowerCase();
    case 'assignee':
      return (row.assignees ?? []).some((a) => a.toLowerCase() === clause.value.toLowerCase());
    case 'label':
      return (row.labels ?? []).some((l) => l.toLowerCase() === clause.value.toLowerCase());
    case 'milestone':
      return (row.milestone ?? '').toLowerCase() === clause.value.toLowerCase();
    case 'field':
      return matchField(row, clause, fieldKinds[clause.key] ?? 'text');
  }
}

export function matchesQuery(row: FilterRow, queryString: string, fieldKinds: Record<string, FieldKind> = {}): boolean {
  const tokens = tokenize(queryString);
  for (const token of tokens) {
    if (!matchClause(row, parseToken(token, fieldKinds), fieldKinds)) return false;
  }
  return true;
}

export function filterRows<T extends FilterRow>(rows: T[], queryString: string, fieldKinds: Record<string, FieldKind> = {}): T[] {
  if (!queryString.trim()) return rows;
  return rows.filter((row) => matchesQuery(row, queryString, fieldKinds));
}
