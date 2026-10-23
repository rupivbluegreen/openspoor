import { describe, expect, it } from 'vitest';
import { formatKpiValue, goalStatus, groupKpis, hasSparkline, trend, trendIsGood, type KpiItem } from './kpi';

describe('formatKpiValue', () => {
  it('formats by unit', () => {
    expect(formatKpiValue(66.6667, '%')).toBe('66.7%');
    expect(formatKpiValue(18, 'h')).toBe('18 h');
    expect(formatKpiValue(19.25, 'ms')).toBe('19.3 ms');
    expect(formatKpiValue(1234.4, 'count')).toBe('1,234');
    expect(formatKpiValue(0, 'count')).toBe('0');
  });
  it('shows an en dash when there is nothing to measure', () => {
    expect(formatKpiValue(null, '%')).toBe('–');
    expect(formatKpiValue(undefined, 'h')).toBe('–');
    expect(formatKpiValue(Number.NaN, 'h')).toBe('–');
  });
});

describe('goalStatus', () => {
  it('higher is better reaches the target', () => {
    expect(goalStatus('higher', 95, 90)).toBe('met');
    expect(goalStatus('higher', 90, 90)).toBe('met');
    expect(goalStatus('higher', 80, 90)).toBe('missed');
  });
  it('lower is better stays under the target', () => {
    expect(goalStatus('lower', 2, 4)).toBe('met');
    expect(goalStatus('lower', 4, 4)).toBe('met');
    expect(goalStatus('lower', 5, 4)).toBe('missed');
  });
  it('is empty without a value or a goal', () => {
    expect(goalStatus('lower', null, 4)).toBe('');
    expect(goalStatus('lower', 3, undefined)).toBe('');
  });
});

describe('trend', () => {
  const pts = (...v: (number | null)[]) => v.map((value, i) => ({ day: `2026-07-${String(i + 1).padStart(2, '0')}`, value }));
  it('compares the first and last day with data', () => {
    expect(trend(pts(null, 1, null, 3))).toBe('up');
    expect(trend(pts(5, 4, 2))).toBe('down');
    expect(trend(pts(2, 9, 2))).toBe('flat');
  });
  it('needs two days of data', () => {
    expect(trend(pts(null, 3, null))).toBeNull();
    expect(trend([])).toBeNull();
  });
  it('judges the change by the KPI direction', () => {
    expect(trendIsGood('higher', 'up')).toBe(true);
    expect(trendIsGood('lower', 'up')).toBe(false);
    expect(trendIsGood('lower', 'down')).toBe(true);
    expect(trendIsGood('higher', 'flat')).toBeNull();
    expect(trendIsGood('higher', null)).toBeNull();
  });
  it('draws a sparkline only with two points', () => {
    expect(hasSparkline(pts(1, null))).toBe(false);
    expect(hasSparkline(pts(1, null, 2))).toBe(true);
    expect(hasSparkline(undefined)).toBe(false);
  });
});

describe('groupKpis', () => {
  it('groups by category in order', () => {
    const k = (id: string, category: string) => ({ id, category }) as KpiItem;
    const g = groupKpis([k('a', 'x'), k('b', 'y'), k('c', 'x')]);
    expect(Object.keys(g)).toEqual(['x', 'y']);
    expect(g.x!.map((i) => i.id)).toEqual(['a', 'c']);
  });
});
