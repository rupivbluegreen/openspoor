import { theme } from 'antd';

type Pos = [number, number];

/** Collects rings (arrays of [lon, lat]) from any GeoJSON object. */
export function geoRings(g: unknown): Pos[][] {
  if (!g) return [];
  if (typeof g === 'string') {
    try {
      return geoRings(JSON.parse(g));
    } catch {
      return [];
    }
  }
  const o = g as { type?: string; coordinates?: unknown; geometry?: unknown; features?: unknown[]; geometries?: unknown[] };
  switch (o.type) {
    case 'FeatureCollection':
      return (o.features ?? []).flatMap(geoRings);
    case 'Feature':
      return geoRings(o.geometry);
    case 'GeometryCollection':
      return (o.geometries ?? []).flatMap(geoRings);
    case 'Polygon':
      return (o.coordinates as Pos[][]) ?? [];
    case 'MultiPolygon':
      return ((o.coordinates as Pos[][][]) ?? []).flat();
    case 'LineString':
      return [(o.coordinates as Pos[]) ?? []];
    case 'Point':
      return [[o.coordinates as Pos]];
    default:
      return [];
  }
}

/** Equirectangular SVG preview of a GeoJSON polygon (no map library). */
export function GeoPreview({ geojson, size = 160 }: { geojson: unknown; size?: number }) {
  const { token } = theme.useToken();
  const rings = geoRings(geojson).filter((r) => r.length > 0 && r.every((p) => Array.isArray(p) && p.length >= 2));
  if (!rings.length) return <span>–</span>;
  const pts = rings.flat();
  const lat0 = pts.reduce((s, p) => s + p[1], 0) / pts.length;
  const k = Math.cos((lat0 * Math.PI) / 180) || 1;
  const xs = pts.map((p) => p[0] * k);
  const ys = pts.map((p) => -p[1]);
  const minX = Math.min(...xs);
  const minY = Math.min(...ys);
  const w = Math.max(...xs) - minX || 1e-6;
  const h = Math.max(...ys) - minY || 1e-6;
  const pad = 8;
  const scale = (size - pad * 2) / Math.max(w, h);
  const proj = (p: Pos) => `${(pad + (p[0] * k - minX) * scale).toFixed(1)},${(pad + (-p[1] - minY) * scale).toFixed(1)}`;
  return (
    <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} role="img" aria-label="plot polygon" style={{ background: token.colorFillQuaternary, borderRadius: 6 }}>
      {rings.map((r, i) =>
        r.length === 1 ? (
          <circle key={i} cx={proj(r[0]).split(',')[0]} cy={proj(r[0]).split(',')[1]} r={4} fill={token.colorPrimary} />
        ) : (
          <polygon key={i} points={r.map(proj).join(' ')} fill={token.colorPrimary} fillOpacity={0.25} stroke={token.colorPrimary} strokeWidth={1.5} />
        ),
      )}
    </svg>
  );
}
