export interface DiffRow {
  path: string;
  before: unknown;
  after: unknown;
}

/** Flat key-level diff of two JSON values. */
export function diffJson(before: unknown, after: unknown, path = ''): DiffRow[] {
  const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v);
  if (isObj(before) && isObj(after)) {
    const keys = [...new Set([...Object.keys(before), ...Object.keys(after)])];
    return keys.flatMap((k) => diffJson(before[k], after[k], path ? `${path}.${k}` : k));
  }
  return JSON.stringify(before) === JSON.stringify(after) ? [] : [{ path: path || '(value)', before, after }];
}

