# Open Spoor frontend

The web frontends for Open Spoor, the Apache-2.0 "Open Agentic Supply Chain OS" (Trace · Link · Act · Comply).
They are built against the HTTP contract in [`../docs/api-contract.md`](../docs/api-contract.md).

| App | Path | Dev port | Audience |
|---|---|---|---|
| `@openspoor/console` | `apps/console` | 5173 | Tenant users: ProLayout shell with the Trace, Link, Act, Comply, Food and Settings sections |
| `@openspoor/operator` | `apps/operator` | 5174 | Node operators: tenants, plans, quotas and consented impersonation |
| `@openspoor/verify` | `apps/verify` | 5175 | Public, no login, mobile-first: scan-and-verify (`/`) and the EPCIS Playground (`/playground`) |
| `@openspoor/ui` | `packages/ui` | – | Shared theme, providers, API client, i18n, GS1 helpers and wrapped ProComponents |

## Running

Requirements: Node 22+ and pnpm 10.

```bash
cd frontend
pnpm install
pnpm dev:console        # http://localhost:5173  (also: dev:operator, dev:verify)
pnpm build              # pnpm -r build: vite build for the apps, tsc --noEmit for packages/ui
pnpm typecheck          # pnpm -r typecheck (tsc --noEmit, strict)
pnpm test               # pnpm -r test (Vitest in packages/ui)
pnpm lint               # currently an alias for typecheck (no ESLint, see "Deviations")
```

Each Vite dev and preview server proxies `/api` to `http://localhost:8080`, where the Go backend runs.
To point it somewhere else, set `OPENSPOOR_API`, for example `OPENSPOOR_API=http://10.0.0.5:8080 pnpm dev:console`.
In production, serve each app's `dist/` from the same origin as the API, or put a reverse proxy in front that routes `/api/v1`.

## Structure

```
frontend/
  package.json              root scripts (dev:console, build, typecheck, test, lint)
  pnpm-workspace.yaml       apps/*, packages/*
  tsconfig.base.json        strict TS settings shared by every package
  packages/ui/src/
    index.ts                core entry, without ProComponents (the verify app uses only this)
    pro-entry.tsx           "@openspoor/ui/pro": core + ProComponents wrappers + ProAppProvider
    pro.ts                  the ONLY module that imports @ant-design/pro-components (swap point)
    theme.ts                design tokens, light/dark algorithm, runtime brand colour override
    AppProvider.tsx         ConfigProvider + antd App + i18n + TanStack Query + theme/lang/dir context
    api/client.ts           fetch client, token store, {items,total} normalisation, Idempotency-Key helper
    api/errors.ts           problem+json → ApiError → notification text
    api/types.ts            domain types (fields the contract leaves unspecified are optional)
    i18n/{en,nl,ar}.ts      typed message catalogues (nl/ar must have the same shape as en)
    gs1.ts                  GTIN-14 / GLN-13 / SSCC check digits + antd form validators
    components/             QueryTable, ESignatureModal/useESignature, CodeView, SvgGraph, JsonSchemaFields,
                            EventErrors, LoginCard, StatusTag, LanguageSwitch, ThemeToggle, Logo
  apps/console/src/
    nav.tsx                 menu + lazy route table, grouped by pillar (Food only when tenant.verticals has "food")
    layout/Shell.tsx        ProLayout with tenant switcher, EN/NL/AR, light/dark, user menu
    components/CrudPage.tsx generic ProTable (via TanStack Query) + ModalForm create/edit page
    pages/{trace,link,act,comply,food,settings}/*.tsx
  apps/operator/src/        login (requires principal.operator), tenants table, edit, impersonate
  apps/verify/src/          Scan.tsx (BarcodeDetector + manual), Playground.tsx, sample.ts
```

### Conventions

- **Server data.** Every list is a ProTable fed by TanStack Query through `QueryTable` (queryKey, fetcher returning `{items,total}`). After a mutation, the query keys are invalidated through `useSubmit` or `useAction` in `apps/console/src/lib/hooks.ts`.
- **Errors.** `QueryCache` and `MutationCache` show the problem+json `title` as an antd notification, with `detail` and the first entries of `errors[]` in the description. Queries tagged `meta: { silent: true }` handle their own errors.
- **Forms.** Forms use ProForm, ModalForm and DrawerForm with validation. GTIN and GLN fields use `gtinValidator` and `glnValidator` from `packages/ui/src/gs1.ts`.
- **E-signatures.** `ESignatureModal` (password re-auth, meaning and reason, then `POST /esignatures`) is reusable. The `useESignature()` hook wraps it so a caller can write `await requestSignature({entity, entityId})`. It is used in the E-signatures page, Agent approvals ("Sign & approve"), Recalls and tenant deletion.
- **Theme.** There is one ConfigProvider theme, with a deep teal-green `#0B6E63` primary ("spoor" is Dutch for track or trace). Light/dark mode is stored per browser. `GET /tenant` → `brand.primaryColor` overrides `colorPrimary` at runtime, and so does saving on the Branding page.
- **i18n.** The languages are English (the default), Dutch and Arabic. Arabic sets `direction="rtl"` on ConfigProvider and `<html dir="rtl">`. antd, ProComponents and dayjs locales switch with the language. Navigation, common actions, common field labels and the verify/playground/operator strings are translated. Some page-specific labels (column headers such as "Capacity" and help texts) are still English literals.
- **Routing.** Every route is lazy-loaded with `React.lazy`.

## Pinned versions

All versions are exact (`.npmrc` has `save-exact=true`). They were checked with `npm view <pkg> dist-tags` on 2026-10-22.

| Package | Version | Note |
|---|---|---|
| react / react-dom | 19.3.1 | latest |
| typescript | 7.0.2 | latest; native `tsc` |
| vite | 8.3.5 | latest |
| @vitejs/plugin-react | 6.1.2 | latest (peer: vite ^8) |
| antd | 6.6.7 | latest |
| @ant-design/pro-components | **3.1.15-7** | prerelease (`beta` tag); see below |
| @ant-design/icons | 6.3.4 | latest |
| @ant-design/charts | 2.6.7 | latest |
| @ant-design/x | 2.9.0 | latest (peer: antd ^6.1.1) |
| @tanstack/react-query | 5.104.5 | latest |
| react-router | 8.5.0 | latest (peer: react >=19.2.7) |
| i18next | 26.5.6 | latest |
| react-i18next | 17.1.3 | latest |
| dayjs | 1.11.23 | latest |
| vitest | 5.0.5 | latest |
| @types/react, @types/react-dom | 19.3.0 | |
| @types/node | 26.6.5 | |

`pnpm install` reports no peer-dependency conflicts with React 19 or antd 6.

## Deviations and decisions

- **ProComponents v3 is not stable yet.** The `latest` tag is 2.8.10, which targets antd 5. The v3 line, which supports antd 6, is only published as prereleases, and the newest is `3.1.15-7`. That is the version used here. All ProComponents imports go through `packages/ui/src/pro.ts`, re-exported as `@openspoor/ui/pro`, so moving to a stable 3.x is a one-file change. Two v3 API differences showed up: columns use `search: false` (there is no `hideInSearch`), and menu data uses `children` (not `routes`).
- **Split entry for the public app.** `@openspoor/ui` (core) does not import ProComponents. `@openspoor/ui/pro` adds them, plus `ProAppProvider` (AppProvider wrapped in ProConfigProvider for dark mode and locale). The console and operator import from `/pro`. The verify app imports only the core entry and does not depend on pro-components.
- **Bundle sizes.**
  - `@ant-design/charts` re-exports `@ant-design/graphs` (G6), so the lazily loaded Dashboard chunk is about 1.46 MB minified (about 426 KB gzip). It loads only when the Dashboard is opened.
  - The shared ProComponents chunk in the console is about 870 KB minified (about 275 KB gzip).
  - The verify entry chunk is about 1.15 MB minified (about 370 KB gzip): antd core, react-dom and react-router.
  - `chunkSizeWarningLimit` is set to 1600 KB, so these are reported in this README rather than as build warnings. Importing `@ant-design/plots` directly would shrink the dashboard chunk.
- **There is no ESLint.** `pnpm lint` runs the strict typecheck (`noUnusedLocals` and `noUnusedParameters` are on).
- **Tests.** Only `packages/ui` has unit tests:
  - GTIN, GLN and SSCC check digits and the form validator;
  - API client: problem+json mapping, plain-text and network errors, the 401 hook, query strings, list normalisation;
  - i18n: en, nl and ar have identical key sets, no empty strings, and every nav label differs from English in Arabic.

  The apps' `test` scripts are no-ops. During development all three apps were also smoke-tested in headless Chromium (Playwright) against a throwaway mock API. That covered login, all 41 console routes, the RTL switch, dark mode with NL, the GLN check-digit message, the operator tenants table, verify scan and playground. No page errors were found. The scripts were not committed.

## Contract gaps and assumptions

Where the contract does not pin down a shape, the UI reads fields with optional chaining and accepts several shapes instead of guessing one:

- `GET /me/tenants` returns a bare array in the current backend. `api.list()` normalises arrays and `{items,total}` alike.
- `/audit` is the only list paged on the server (`limit` and `offset`). The other lists are paged in the browser. Filters the contract does not define (shipment direction, report connector or status, operator tenant search) are applied client-side.
- Capture errors, playground errors and problem `errors[]` may be strings or `{index, eventId, field|path, message}`, and both are rendered.
- `GET /epcis/events` returns an EPCISQueryDocument. Events are read from `epcisBody.queryResults.resultsBody.eventList`, falling back to `eventList` or `items`. For paging, `nextPageToken` is taken from the body if present. Response headers are not read.
- **Request bodies the contract does not specify.** The UI sends the following; please confirm against the backend:

  | Endpoint | Body sent |
  |---|---|
  | `POST /epcis/queries` | `{name, query}` (EPCIS 2.0 style) |
  | `POST /shipments/inbound` | `{document, asn}` |
  | `POST /suppliers/certificates` | `{partnerId, kind, number, issuer, validFrom, validTo}` |
  | `POST /federation/peers` | `{name, url, publicKey}` |
  | `POST /food/recalls` | `{title, classification, tlcs[], reason}` |
  | `POST /food/eudr/dds` | `{commodity, hsCode, quantity, plotIds[], countryOfProduction}` |
  | `PATCH /cases/{id}` | `{status, priority, assignee, note}` |
  | `POST /workflows` | `{name, ...definition}` (from a JSON editor) |
  | `POST /forms` | `{name, schema}`, where `schema` is a JSON Schema built by the designer |
  | `DELETE /tenant` | `{signatureId}`, sent after an e-signature |
  | `POST /operator/tenants/{id}/impersonate` | the response is shown as raw JSON, because its shape is not specified |

- `/connectors` items may carry `configSchema` or `schema`. Instance configuration forms are generated from that JSON Schema by `JsonSchemaFields`.
- `/verify/scan`: a `404` problem is shown as "Not found". Otherwise `status` is mapped to one of five outcomes: verified, suspect, not found, recalled or expired.
- **"Sign in with SSO"** appears only when `GET /meta` returns `oidc: true`. It is a placeholder, because the OIDC redirect endpoint is not in the v1 contract.
- **Sessions.** The operator app stores its token under a different key (`openspoor.operator.token`) from the console (`openspoor.token`). A 401 clears the session.
