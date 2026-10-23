# verify

Public, unauthenticated surfaces: pharmacy/consumer "scan and verify" and the EPCIS
Playground (validate, convert, visualize). All routes are rate-limited per client IP
(5 requests/s, burst 30).

A scan decodes the code, finds the brand owner on this node (the tenant with a serial pool
for the GTIN, else any tenant with the product) and checks the pack against that tenant's
records. Results: `verified`, `unknown_product`, `not_found`, `suspect` (no serial, batch or
expiry mismatch), `recalled`, `decommissioned`, `expired`. Any result other than `verified`
raises a deduplicated alert for the brand owner.

The module also hosts the GS1 Digital Link resolver (see below).

## Endpoints (`/api/v1`, public)

| Method | Path | Purpose |
|---|---|---|
| POST | `/verify/scan` | Verify a scanned pack (`{"code": ...}`) |
| POST | `/playground/validate` | Validate an EPCIS document and report field errors |
| POST | `/playground/convert` | Convert between EPCIS JSON-LD and XML |
| POST | `/playground/visualize` | Build a node/edge graph of a document's objects and events |

| GET | `/01/{gtin}[/10/{lot}][/21/{serial}]` and `/api/v1/resolve/...` | Digital Link resolver (public, rate-limited; credentials select a non-consumer audience) |

Authenticated routes (`/api/v1`, Admin / Compliance, Viewer reads with GET): `/consumer-info`, `/verify/stats`,
`/verify/reports`, `/resolver/link-types` (GET), `/resolver/links` (GET, POST) and `/resolver/links/{id}` (PUT, DELETE;
Admin, Compliance; audited).

## Digital Link resolver

A GS1 Digital Link on a GTIN resolves to typed links owned by the brand owner (found like a scan:
the tenant with a serial pool for the GTIN). Stored links (`resolver_link`) have a link type, audiences,
media type, language, an https address, an optional lot and a default flag; links generated from existing
data are added: the patient leaflet from `consumer_info.leaflet_url` (per language), the consumer verification
page (when the link names a serial) and, for a lot with an open recall, a recall status link.

- Link types: `gs1:pip` (product information), `gs1:safetyInfo`, `gs1:recallStatus` (IRIs
  `https://gs1.org/voc/<name>`), local `openspoor:leaflet` and `openspoor:consumerVerification`, and
  `custom:<name>`. `TODO(spec)`: confirm the `gs1:` identifiers against the current GS1 vocabulary.
- Audiences: `consumer` (anonymous), `trading_partner` (the owner's users, active linked partner tenants),
  `authority` (the owner's Admin and Compliance credentials). A consumer link is visible to every audience.
  Select with `?audience=`; credentials are the usual bearer token or API key. `TODO(spec)`: how an authority
  identifies itself.
- Negotiation: `?linkType=all` or `Accept: application/linkset+json` returns an RFC 9264 linkset;
  `Accept: application/json` a plain summary; otherwise 307 to the best link by `Accept` and
  `Accept-Language` (`?linkType=` limits the type; without it the default link, else consumer verification,
  else product information). `Link` headers (RFC 8288) list the links.
- A product passport registry client is not built (specification not final).

## Background work

None.

## Data

Own tables: `consumer_info` and friends (migration `00012_consumer.sql`) and `resolver_link` (migration `00030_trace_integrity.sql`). Reads `product`, `serial_pool`, `serial_state`, `partner` (audience check) and `lot_recall` (recall status link); writes `alert`.

## Depends on

`core/*`, `masterdata`, `serialization` (code parsing), `trace` (serial state), `pkg/epcis`,
`pkg/gs1`.

## Spec gaps

`TODO(spec)`: GS1 link type identifiers, authority identification for the resolver (see `docs/OPEN_QUESTIONS.md`).

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/verify`.
