import { useMemo } from 'react';
import { theme } from 'antd';

export interface GraphNode {
  id: string;
  label?: string;
  kind?: string;
}
export interface GraphEdge {
  from: string;
  to: string;
  label?: string;
}

const NODE_W = 168;
const NODE_H = 40;
const COL_GAP = 72;
const ROW_GAP = 20;
const PAD = 16;

/** Layered left-to-right layout (longest path from sources), cycle-safe. */
export function layoutGraph(nodes: GraphNode[], edges: GraphEdge[]) {
  const ids = new Set(nodes.map((n) => n.id));
  const all = [...nodes];
  for (const e of edges) {
    for (const id of [e.from, e.to]) {
      if (!ids.has(id)) {
        ids.add(id);
        all.push({ id });
      }
    }
  }
  const depth = new Map<string, number>(all.map((n) => [n.id, 0]));
  for (let i = 0; i < all.length; i++) {
    let changed = false;
    for (const e of edges) {
      const d = (depth.get(e.from) ?? 0) + 1;
      if (d > (depth.get(e.to) ?? 0) && d < all.length) {
        depth.set(e.to, d);
        changed = true;
      }
    }
    if (!changed) break;
  }
  const cols = new Map<number, GraphNode[]>();
  for (const n of all) {
    const d = depth.get(n.id) ?? 0;
    cols.set(d, [...(cols.get(d) ?? []), n]);
  }
  const pos = new Map<string, { x: number; y: number }>();
  let maxRows = 0;
  for (const [d, list] of cols) {
    maxRows = Math.max(maxRows, list.length);
    list.forEach((n, i) => pos.set(n.id, { x: PAD + d * (NODE_W + COL_GAP), y: PAD + i * (NODE_H + ROW_GAP) }));
  }
  const width = PAD * 2 + Math.max(1, cols.size) * (NODE_W + COL_GAP) - COL_GAP;
  const height = PAD * 2 + Math.max(1, maxRows) * (NODE_H + ROW_GAP) - ROW_GAP;
  return { nodes: all, pos, width, height };
}

function short(s: string, n = 24): string {
  return s.length > n ? `…${s.slice(-(n - 1))}` : s;
}

export function SvgGraph({ nodes, edges, emptyText = '–' }: { nodes: GraphNode[]; edges: GraphEdge[]; emptyText?: string }) {
  const { token } = theme.useToken();
  const layout = useMemo(() => layoutGraph(nodes, edges), [nodes, edges]);
  if (layout.nodes.length === 0) return <div>{emptyText}</div>;
  const palette = [token.colorPrimary, token.colorInfo, token.colorWarning, token.colorSuccess, token.colorError];
  const kinds = [...new Set(layout.nodes.map((n) => n.kind ?? ''))];
  return (
    <div dir="ltr" style={{ overflow: 'auto', maxWidth: '100%' }}>
      <svg width={layout.width} height={layout.height} role="img" aria-label="graph" style={{ display: 'block' }}>
        <defs>
          <marker id="os-arrow" viewBox="0 0 10 10" refX="10" refY="5" markerWidth="8" markerHeight="8" orient="auto-start-reverse">
            <path d="M 0 0 L 10 5 L 0 10 z" fill={token.colorTextTertiary} />
          </marker>
        </defs>
        {edges.map((e, i) => {
          const a = layout.pos.get(e.from);
          const b = layout.pos.get(e.to);
          if (!a || !b) return null;
          const x1 = a.x + NODE_W;
          const y1 = a.y + NODE_H / 2;
          const x2 = b.x;
          const y2 = b.y + NODE_H / 2;
          const mx = (x1 + x2) / 2;
          return (
            <g key={i}>
              <path d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`} fill="none" stroke={token.colorTextTertiary} markerEnd="url(#os-arrow)" />
              {e.label && (
                <text x={mx} y={(y1 + y2) / 2 - 4} fontSize={10} textAnchor="middle" fill={token.colorTextSecondary}>
                  {short(e.label, 18)}
                </text>
              )}
            </g>
          );
        })}
        {layout.nodes.map((n) => {
          const p = layout.pos.get(n.id)!;
          const color = palette[kinds.indexOf(n.kind ?? '') % palette.length];
          return (
            <g key={n.id} transform={`translate(${p.x},${p.y})`}>
              <title>{`${n.kind ? `${n.kind}: ` : ''}${n.label ?? n.id}`}</title>
              <rect width={NODE_W} height={NODE_H} rx={6} fill={token.colorBgContainer} stroke={color} strokeWidth={1.5} />
              <rect width={4} height={NODE_H} rx={2} fill={color} />
              <text x={10} y={16} fontSize={10} fill={token.colorTextSecondary}>
                {n.kind ?? ''}
              </text>
              <text x={10} y={31} fontSize={12} fill={token.colorText}>
                {short(n.label ?? n.id)}
              </text>
            </g>
          );
        })}
      </svg>
    </div>
  );
}
