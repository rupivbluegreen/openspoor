/**
 * GS1 helpers: mod-10 check digit for GTIN / GLN / SSCC, plus form validators.
 * The weight pattern is 3,1,3,1… counted from the right-most data digit.
 */
export function gs1CheckDigit(data: string): number {
  if (!/^\d+$/.test(data)) throw new Error('GS1 data must be numeric');
  let sum = 0;
  for (let i = 0; i < data.length; i++) {
    const digit = data.charCodeAt(data.length - 1 - i) - 48;
    sum += digit * (i % 2 === 0 ? 3 : 1);
  }
  return (10 - (sum % 10)) % 10;
}

/** True when `code` is all digits, has `length` digits and a valid check digit. */
export function hasValidCheckDigit(code: string, length?: number): boolean {
  if (!/^\d+$/.test(code)) return false;
  if (length !== undefined && code.length !== length) return false;
  if (code.length < 2) return false;
  return gs1CheckDigit(code.slice(0, -1)) === Number(code[code.length - 1]);
}

/** GTIN-14 (also accepts GTIN-8/12/13 when `allowShort`, left-padded to 14). */
export function isValidGtin(code: string, allowShort = false): boolean {
  const c = code.trim();
  if (allowShort && [8, 12, 13].includes(c.length)) return hasValidCheckDigit(c.padStart(14, '0'), 14);
  return hasValidCheckDigit(c, 14);
}

export function isValidGln(code: string): boolean {
  return hasValidCheckDigit(code.trim(), 13);
}

export function isValidSscc(code: string): boolean {
  return hasValidCheckDigit(code.trim(), 18);
}

/** Pads a GTIN-8/12/13 to GTIN-14. Returns the input unchanged otherwise. */
export function toGtin14(code: string): string {
  const c = code.trim();
  return /^\d{8}$|^\d{12,13}$/.test(c) ? c.padStart(14, '0') : c;
}

/** Appends the check digit to 13 (GTIN) / 12 (GLN) data digits. */
export function withCheckDigit(data: string): string {
  return data + String(gs1CheckDigit(data));
}

type Validator = (_rule: unknown, value: unknown) => Promise<void>;

/** antd Form rule validator for a GTIN-14. Empty values pass (combine with `required`). */
export function gtinValidator(message = 'Invalid GTIN-14 (check digit)'): { validator: Validator } {
  return {
    validator: (_r, v) =>
      v === undefined || v === null || v === '' || isValidGtin(String(v)) ? Promise.resolve() : Promise.reject(new Error(message)),
  };
}

/** antd Form rule validator for a GLN-13. Empty values pass (combine with `required`). */
export function glnValidator(message = 'Invalid GLN-13 (check digit)'): { validator: Validator } {
  return {
    validator: (_r, v) =>
      v === undefined || v === null || v === '' || isValidGln(String(v)) ? Promise.resolve() : Promise.reject(new Error(message)),
  };
}
