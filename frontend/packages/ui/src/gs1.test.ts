import { describe, expect, it } from 'vitest';
import { gs1CheckDigit, gtinValidator, isValidGln, isValidGtin, isValidSscc, toGtin14, withCheckDigit } from './gs1';

describe('GS1 check digits', () => {
  it('computes the mod-10 check digit', () => {
    expect(gs1CheckDigit('0950600013435')).toBe(2); // GTIN-14 09506000134352
    expect(gs1CheckDigit('061414100000')).toBe(5); // GLN 0614141000005
  });

  it('validates GTIN-14', () => {
    expect(isValidGtin('09506000134352')).toBe(true);
    expect(isValidGtin('09506000134353')).toBe(false);
    expect(isValidGtin('0950600013435')).toBe(false); // 13 digits
    expect(isValidGtin('0950600013435A')).toBe(false);
    expect(isValidGtin('00012345600012')).toBe(true);
  });

  it('accepts short GTINs when allowed', () => {
    expect(isValidGtin('4006381333931', true)).toBe(true); // EAN-13
    expect(isValidGtin('036000291452', true)).toBe(true); // UPC-A
    expect(toGtin14('4006381333931')).toBe('04006381333931');
  });

  it('validates GLN-13', () => {
    expect(isValidGln('0614141000005')).toBe(true);
    expect(isValidGln('0614141000006')).toBe(false);
    expect(isValidGln(withCheckDigit('061414100000'))).toBe(true);
    expect(isValidGln('5412345000013')).toBe(true);
    expect(isValidGln('5412345000014')).toBe(false);
    expect(isValidGln('541234500001')).toBe(false);
  });

  it('validates SSCC-18', () => {
    expect(isValidSscc('106141411234567897')).toBe(true);
    expect(isValidSscc('106141411234567890')).toBe(false);
  });

  it('form validator resolves for empty and valid, rejects invalid', async () => {
    const v = gtinValidator('bad');
    await expect(v.validator({}, '')).resolves.toBeUndefined();
    await expect(v.validator({}, '09506000134352')).resolves.toBeUndefined();
    await expect(v.validator({}, '09506000134350')).rejects.toThrow('bad');
  });
});
