import { downloadBlob } from './files';

/** UTF-8 byte order mark: lets spreadsheet apps detect UTF-8 (Arabic, Dutch diacritics). */
export const CSV_BOM = '﻿';

/** Formats one value as a CSV cell (RFC 4180 quoting; objects become JSON). */
export function csvCell(v: unknown): string {
  if (v === undefined || v === null) return '';
  let s: string;
  if (typeof v === 'string') s = v;
  else if (typeof v === 'number' || typeof v === 'boolean' || typeof v === 'bigint') s = String(v);
  else if (v instanceof Date) s = v.toISOString();
  else if (Array.isArray(v) && v.every((x) => x === null || typeof x !== 'object')) s = v.map((x) => (x === null || x === undefined ? '' : String(x))).join('; ');
  else s = JSON.stringify(v) ?? '';
  // Spreadsheet formula injection guard: text cells must not start a formula.
  if (typeof v === 'string' && /^[=+@\t\r]/.test(s)) s = `'${s}`;
  return /[",\r\n]/.test(s) || s !== s.trim() ? `"${s.replace(/"/g, '""')}"` : s;
}

/** Serialises a header row plus data rows to CSV text with a leading BOM and CRLF line endings. */
export function toCsv(header: readonly unknown[], rows: readonly (readonly unknown[])[]): string {
  return CSV_BOM + [header, ...rows].map((r) => r.map(csvCell).join(',')).join('\r\n') + '\r\n';
}

/** `<name>-<yyyy-mm-dd>.csv` using the local date. */
export function csvFilename(name: string, now = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${name}-${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}.csv`;
}

export function downloadCsv(name: string, header: readonly unknown[], rows: readonly (readonly unknown[])[]): void {
  downloadBlob(toCsv(header, rows), csvFilename(name), 'text/csv;charset=utf-8');
}
