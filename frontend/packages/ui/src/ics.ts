/**
 * Minimal iCalendar (RFC 5545) writer for the consumer expiry reminder. Everything happens in
 * the browser: no network call, no tracking identifiers beyond a random UID the format requires.
 */

const CRLF = '\r\n';

/** Escapes a TEXT value: backslash, semicolon, comma and line breaks (RFC 5545 §3.3.11). */
export function escapeIcsText(v: string): string {
  return v
    .replace(/\\/g, '\\\\')
    .replace(/;/g, '\\;')
    .replace(/,/g, '\\,')
    .replace(/\r\n|\r|\n/g, '\\n');
}

/** Folds a content line at 75 octets (UTF-8), continuing with CRLF + one space (RFC 5545 §3.1). */
export function foldIcsLine(line: string): string {
  const enc = new TextEncoder();
  const parts: string[] = [];
  let cur = '';
  let octets = 0;
  for (const ch of line) {
    const n = enc.encode(ch).length;
    // The first line holds 75 octets; continuation lines start with a space, so 74 more.
    const limit = parts.length === 0 ? 75 : 74;
    if (octets + n > limit) {
      parts.push(cur);
      cur = '';
      octets = 0;
    }
    cur += ch;
    octets += n;
  }
  parts.push(cur);
  return parts.join(`${CRLF} `);
}

/** Parses YYYY-MM-DD as a calendar date (UTC midnight), or null. */
export function parseIsoDate(v: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(v);
  if (!m) return null;
  const d = new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3])));
  return d.getUTCFullYear() === Number(m[1]) && d.getUTCMonth() === Number(m[2]) - 1 && d.getUTCDate() === Number(m[3]) ? d : null;
}

/** A DATE value (YYYYMMDD) for an all-day event. */
export function icsDate(d: Date): string {
  return `${d.getUTCFullYear()}${String(d.getUTCMonth() + 1).padStart(2, '0')}${String(d.getUTCDate()).padStart(2, '0')}`;
}

/** A UTC DATE-TIME value (YYYYMMDDTHHMMSSZ). */
export function icsDateTime(d: Date): string {
  return d.toISOString().replace(/[-:]/g, '').replace(/\.\d{3}/, '');
}

function addDays(d: Date, n: number): Date {
  return new Date(d.getTime() + n * 86_400_000);
}

export interface ExpiryReminder {
  /** Expiry date of the pack, YYYY-MM-DD. */
  expiryDate: string;
  /** Event title, e.g. "Paracetamol 500 mg expires". */
  summary: string;
  description?: string;
  /** Days before expiry for the reminder (default 7). */
  daysBefore?: number;
  /** Clock and UID source, injectable for tests. */
  now?: Date;
  uid?: string;
}

function randomUid(): string {
  const c = globalThis.crypto as Crypto | undefined;
  const id = c && typeof c.randomUUID === 'function' ? c.randomUUID() : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
  return `${id}@openspoor.local`;
}

/**
 * Builds a one-event calendar: an all-day event `daysBefore` days before expiry (not earlier
 * than today), CRLF line endings, folded lines. Returns null for an invalid date.
 */
export function buildExpiryIcs(r: ExpiryReminder): string | null {
  const expiry = parseIsoDate(r.expiryDate);
  if (!expiry) return null;
  const now = r.now ?? new Date();
  const today = new Date(Date.UTC(now.getFullYear(), now.getMonth(), now.getDate()));
  let start = addDays(expiry, -(r.daysBefore ?? 7));
  if (start < today && today <= expiry) start = today;
  const lines = [
    'BEGIN:VCALENDAR',
    'VERSION:2.0',
    'PRODID:-//Open Spoor//Verify//EN',
    'CALSCALE:GREGORIAN',
    'METHOD:PUBLISH',
    'BEGIN:VEVENT',
    `UID:${r.uid ?? randomUid()}`,
    `DTSTAMP:${icsDateTime(now)}`,
    `DTSTART;VALUE=DATE:${icsDate(start)}`,
    `DTEND;VALUE=DATE:${icsDate(addDays(start, 1))}`,
    `SUMMARY:${escapeIcsText(r.summary)}`,
    ...(r.description ? [`DESCRIPTION:${escapeIcsText(r.description)}`] : []),
    'TRANSP:TRANSPARENT',
    'END:VEVENT',
    'END:VCALENDAR',
  ];
  return lines.map(foldIcsLine).join(CRLF) + CRLF;
}

/** A safe download name for the reminder file. */
export function icsFilename(product: string): string {
  const base = product.normalize('NFKD').replace(/[^\w-]+/g, '-').replace(/^-+|-+$/g, '').toLowerCase().slice(0, 60);
  return `${base || 'pack'}-expiry.ics`;
}
