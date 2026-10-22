import { describe, expect, it } from 'vitest';
import { ar, en, flattenKeys, nl } from './index';

describe('i18n resources', () => {
  const base = flattenKeys(en).sort();

  it.each([
    ['nl', nl],
    ['ar', ar],
  ])('%s has every English key and no extras', (_name, msgs) => {
    expect(flattenKeys(msgs as Record<string, unknown>).sort()).toEqual(base);
  });

  it('has no empty translations', () => {
    for (const msgs of [en, nl, ar]) {
      const walk = (o: Record<string, unknown>): void =>
        Object.values(o).forEach((v) => (typeof v === 'string' ? expect(v.trim()).not.toBe('') : walk(v as Record<string, unknown>)));
      walk(msgs);
    }
  });

  it('translates navigation labels (not just copies of English)', () => {
    const navKeys = Object.keys(en.nav) as (keyof typeof en.nav)[];
    const arDiffers = navKeys.filter((k) => ar.nav[k] !== en.nav[k]).length;
    expect(arDiffers).toBe(navKeys.length);
  });
});
