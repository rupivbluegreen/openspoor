import { describe, expect, it } from 'vitest';
import { overviewShapes } from './plotOverview';

describe('overviewShapes', () => {
  it('returns nothing for unreadable geometry', () => {
    expect(overviewShapes([{ name: 'a' }, { name: 'b', geojson: { type: 'LineString' } }, { name: 'c', geojson: null }])).toEqual([]);
  });

  it('fits a polygon and a feature point inside the viewport', () => {
    const shapes = overviewShapes([
      { name: 'field', geojson: { type: 'Polygon', coordinates: [[[5, 52], [5.1, 52], [5.1, 52.1], [5, 52.1], [5, 52]]] } },
      { name: 'pin', geojson: { type: 'Feature', geometry: { type: 'Point', coordinates: [5.05, 52.05] } } },
    ]);
    expect(shapes.map((s) => s.name)).toEqual(['field', 'pin']);
    expect(shapes[1].isPoint).toBe(true);
    for (const [x, y] of shapes.flatMap((s) => s.points)) {
      expect(x).toBeGreaterThanOrEqual(0);
      expect(x).toBeLessThanOrEqual(720);
      expect(y).toBeGreaterThanOrEqual(0);
      expect(y).toBeLessThanOrEqual(420);
    }
  });

  it('handles a single point', () => {
    const [s] = overviewShapes([{ name: 'one', geojson: { type: 'Point', coordinates: [1, 2] } }]);
    expect(s.points).toHaveLength(1);
  });
});
