# ADR-003: Ant Design system only for the UI

- Status: accepted
- Date: 2026-10-22

## Decision
React 19 + TypeScript + Vite + React Router + TanStack Query. UI components come only from antd 6, @ant-design/pro-components, @ant-design/icons, @ant-design/charts and @ant-design/x (assistant and agent approvals). No Tailwind or other kits. One ConfigProvider theme with Open Spoor tokens, light/dark, per-tenant brand colour at runtime. i18next with English, Dutch and Arabic (RTL). Exact versions are pinned; ProComponents usage is wrapped in `packages/ui` so a prerelease can be swapped.

## Consequences
Dense, consistent enterprise screens (tables, forms, steps) with little custom CSS; RTL and locale support come from antd.
