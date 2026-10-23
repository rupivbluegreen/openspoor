/** KPI library: API shapes and the small pure helpers the KPI dashboards share. */

export interface KpiBucket {
  label: string;
  count: number;
}

export interface KpiGoal {
  target: number;
  note?: string;
  updatedBy?: string;
  updatedAt?: string;
}

export type KpiDirection = 'higher' | 'lower';
export type KpiGoalStatus = 'met' | 'missed' | '';

export interface KpiItem {
  id: string;
  name: string;
  category: string;
  unit: string;
  direction: KpiDirection;
  description: string;
  kind: 'ratio' | 'sum' | 'quantile' | 'snapshot';
  value: number | null;
  samples: number;
  breakdown?: KpiBucket[];
  seriesAvailable: boolean;
  peerComparable: boolean;
  goal?: KpiGoal;
  goalStatus?: KpiGoalStatus;
  error?: string;
}

export interface KpiCategory {
  key: string;
  name: string;
  count: number;
}

export interface KpiList {
  items: KpiItem[];
  total: number;
  days: number;
  categories: KpiCategory[];
  generatedAt: string;
}

export interface KpiPoint {
  day: string;
  value: number | null;
}

export interface KpiSeries {
  id: string;
  unit: string;
  direction: KpiDirection;
  days: number;
  available: boolean;
  points: KpiPoint[];
}

/** Formats a KPI value with its unit; null (nothing to measure) is an en dash. */
export function formatKpiValue(value: number | null | undefined, unit: string): string {
  if (value === null || value === undefined || Number.isNaN(value)) return '–';
  const rounded = unit === 'count' ? Math.round(value) : Math.round(value * 10) / 10;
  const n = rounded.toLocaleString('en-US', { maximumFractionDigits: 1 });
  switch (unit) {
    case '%':
      return `${n}%`;
    case 'count':
      return n;
    default:
      return `${n} ${unit}`;
  }
}

/** Whether a value meets a goal: lower-is-better KPIs must stay at or under it, others reach it. */
export function goalStatus(direction: KpiDirection, value: number | null | undefined, target: number | null | undefined): KpiGoalStatus {
  if (value === null || value === undefined || target === null || target === undefined) return '';
  return (direction === 'higher' ? value >= target : value <= target) ? 'met' : 'missed';
}

/** Direction of change between the first and last day that has data: the series' trend. */
export function trend(points: KpiPoint[]): 'up' | 'down' | 'flat' | null {
  const vals = points.filter((p) => p.value !== null).map((p) => p.value as number);
  if (vals.length < 2) return null;
  const d = vals[vals.length - 1]! - vals[0]!;
  return d === 0 ? 'flat' : d > 0 ? 'up' : 'down';
}

/** Whether a trend is an improvement for the KPI's direction (flat counts as neutral: null). */
export function trendIsGood(direction: KpiDirection, t: 'up' | 'down' | 'flat' | null): boolean | null {
  if (t === null || t === 'flat') return null;
  return direction === 'higher' ? t === 'up' : t === 'down';
}

/** True when a series has at least two days with data, enough to draw a line. */
export function hasSparkline(points: KpiPoint[] | undefined): boolean {
  return (points ?? []).filter((p) => p.value !== null).length >= 2;
}

/** Groups KPIs by category keeping the server's order. */
export function groupKpis(items: KpiItem[]): Record<string, KpiItem[]> {
  const out: Record<string, KpiItem[]> = {};
  for (const k of items) (out[k.category] ??= []).push(k);
  return out;
}
