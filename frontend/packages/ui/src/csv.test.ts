import { describe, expect, it } from 'vitest';
import { CSV_BOM, csvCell, csvFilename, toCsv } from './csv';

describe('csv', () => {
  it('starts with a UTF-8 BOM and uses CRLF', () => {
    const out = toCsv(['a', 'b'], [[1, 'x']]);
    expect(out.startsWith(CSV_BOM)).toBe(true);
    expect(out.charCodeAt(0)).toBe(0xfeff);
    expect(out).toBe(`${CSV_BOM}a,b\r\n1,x\r\n`);
  });

  it('quotes commas, quotes and newlines', () => {
    expect(csvCell('a,b')).toBe('"a,b"');
    expect(csvCell('say "hi"')).toBe('"say ""hi"""');
    expect(csvCell('line1\nline2')).toBe('"line1\nline2"');
    expect(csvCell('cr\r\nlf')).toBe('"cr\r\nlf"');
    expect(csvCell(' padded ')).toBe('" padded "');
    expect(csvCell('plain')).toBe('plain');
  });

  it('formats empty, numbers, arrays and objects', () => {
    expect(csvCell(undefined)).toBe('');
    expect(csvCell(null)).toBe('');
    expect(csvCell(0)).toBe('0');
    expect(csvCell(-1.5)).toBe('-1.5');
    expect(csvCell(false)).toBe('false');
    expect(csvCell(['US', 'NL'])).toBe('US; NL');
    expect(csvCell({ a: 1, b: 'x' })).toBe('"{""a"":1,""b"":""x""}"');
  });

  it('keeps non-Latin text intact', () => {
    expect(toCsv(['naam'], [['الشحنات'], ['café, één']])).toBe(`${CSV_BOM}naam\r\nالشحنات\r\n"café, één"\r\n`);
  });

  it('neutralises formula-like text', () => {
    expect(csvCell('=SUM(A1)')).toBe("'=SUM(A1)");
    expect(csvCell('@cmd')).toBe("'@cmd");
  });

  it('builds a dated filename', () => {
    expect(csvFilename('shipments', new Date(2026, 0, 5))).toBe('shipments-2026-01-05.csv');
  });
});
