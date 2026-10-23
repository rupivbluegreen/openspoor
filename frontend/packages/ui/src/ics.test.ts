import { describe, expect, it } from 'vitest';
import { buildExpiryIcs, escapeIcsText, foldIcsLine, icsFilename, parseIsoDate } from './ics';

const now = new Date(Date.UTC(2026, 0, 15, 9, 30, 0));

describe('ics', () => {
  it('escapes backslashes, commas, semicolons and newlines', () => {
    expect(escapeIcsText('a,b;c')).toBe('a\\,b\\;c');
    expect(escapeIcsText('back\\slash')).toBe('back\\\\slash');
    expect(escapeIcsText('l1\nl2\r\nl3\rl4')).toBe('l1\\nl2\\nl3\\nl4');
    expect(escapeIcsText('Paracetamol 500 mg, tablets; 20')).toBe('Paracetamol 500 mg\\, tablets\\; 20');
  });

  it('writes an all-day event a week before expiry with CRLF line endings', () => {
    const out = buildExpiryIcs({ expiryDate: '2026-06-30', summary: 'Aspirin, 100 mg; box expires', description: 'Batch L1\nCheck it', now, uid: 'x@test' })!;
    expect(out).not.toBeNull();
    expect(out.endsWith('\r\n')).toBe(true);
    expect(out.replace(/\r\n/g, '')).not.toMatch(/[\r\n]/);
    const lines = out.split('\r\n');
    expect(lines[0]).toBe('BEGIN:VCALENDAR');
    expect(lines).toContain('VERSION:2.0');
    expect(lines).toContain('DTSTART;VALUE=DATE:20260623');
    expect(lines).toContain('DTEND;VALUE=DATE:20260624');
    expect(lines).toContain('DTSTAMP:20260115T093000Z');
    expect(lines).toContain('UID:x@test');
    expect(lines).toContain('SUMMARY:Aspirin\\, 100 mg\\; box expires');
    expect(lines).toContain('DESCRIPTION:Batch L1\\nCheck it');
    expect(lines.at(-2)).toBe('END:VCALENDAR');
  });

  it('crosses month and year boundaries', () => {
    const out = buildExpiryIcs({ expiryDate: '2027-01-03', summary: 'x', now, uid: 'u' })!;
    expect(out).toContain('DTSTART;VALUE=DATE:20261227\r\n');
    expect(out).toContain('DTEND;VALUE=DATE:20261228\r\n');
  });

  it('does not schedule the reminder in the past when less than a week remains', () => {
    const out = buildExpiryIcs({ expiryDate: '2026-01-18', summary: 'x', now, uid: 'u' })!;
    expect(out).toContain('DTSTART;VALUE=DATE:20260115\r\n');
  });

  it('rejects invalid dates', () => {
    expect(buildExpiryIcs({ expiryDate: '2026-02-30', summary: 'x', now })).toBeNull();
    expect(buildExpiryIcs({ expiryDate: '30-06-2026', summary: 'x', now })).toBeNull();
    expect(parseIsoDate('2028-02-29')).not.toBeNull();
  });

  it('folds long lines at 75 octets without splitting characters', () => {
    const long = `SUMMARY:${'a'.repeat(200)}`;
    const folded = foldIcsLine(long).split('\r\n');
    expect(folded.length).toBeGreaterThan(1);
    expect(folded[0]).toHaveLength(75);
    folded.slice(1).forEach((l) => expect(l.startsWith(' ')).toBe(true));
    expect(folded.map((l, i) => (i ? l.slice(1) : l)).join('')).toBe(long);
    const ar = foldIcsLine(`SUMMARY:${'دواء '.repeat(30)}`);
    for (const l of ar.split('\r\n')) expect(new TextEncoder().encode(l).length).toBeLessThanOrEqual(75);
    expect(ar).not.toContain('�');
  });

  it('makes a safe file name', () => {
    expect(icsFilename('Aspirin 100 mg / box')).toBe('aspirin-100-mg-box-expiry.ics');
    expect(icsFilename('دواء')).toBe('pack-expiry.ics');
  });
});
