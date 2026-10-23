/** Projects registered plots (GeoJSON Point / Polygon / Feature) into a schematic SVG viewport. */
export interface OverviewShape {
  name: string;
  /** Pixel positions; one entry for a point. */
  points: [number, number][];
  isPoint: boolean;
}

export interface OverviewPlot {
  id?: string;
  name: string;
  geojson?: unknown;
}

type Coords = number[];

function geometryOf(g: unknown): { type?: string; coordinates?: unknown } | null {
  if (typeof g !== 'object' || g === null) return null;
  const o = g as { type?: string; geometry?: unknown; coordinates?: unknown };
  if (o.type === 'Feature') return geometryOf(o.geometry);
  return o;
}

export function overviewShapes(plots: OverviewPlot[], width = 720, height = 420, pad = 30): OverviewShape[] {
  const raw: { name: string; ring: [number, number][]; isPoint: boolean }[] = [];
  for (const p of plots) {
    const g = geometryOf(p.geojson);
    if (!g) continue;
    if (g.type === 'Point' && Array.isArray(g.coordinates) && g.coordinates.length >= 2) {
      const c = g.coordinates as Coords;
      raw.push({ name: p.name, ring: [[c[0], c[1]]], isPoint: true });
    } else if (g.type === 'Polygon' && Array.isArray(g.coordinates) && Array.isArray(g.coordinates[0])) {
      const ring = (g.coordinates[0] as Coords[]).filter((c) => Array.isArray(c) && c.length >= 2).map((c) => [c[0], c[1]] as [number, number]);
      if (ring.length) raw.push({ name: p.name, ring, isPoint: false });
    }
  }
  if (!raw.length) return [];
  const all = raw.flatMap((r) => r.ring);
  const minX = Math.min(...all.map((c) => c[0]));
  const maxX = Math.max(...all.map((c) => c[0]));
  const minY = Math.min(...all.map((c) => c[1]));
  const maxY = Math.max(...all.map((c) => c[1]));
  const kx = Math.cos((((minY + maxY) / 2) * Math.PI) / 180);
  const spanX = Math.max((maxX - minX) * kx, 1e-6);
  const spanY = Math.max(maxY - minY, 1e-6);
  const scale = Math.min((width - 2 * pad) / spanX, (height - 2 * pad) / spanY);
  return raw.map((r) => ({
    name: r.name,
    isPoint: r.isPoint,
    points: r.ring.map(([x, y]) => [pad + (x - minX) * kx * scale, height - pad - (y - minY) * scale] as [number, number]),
  }));
}
