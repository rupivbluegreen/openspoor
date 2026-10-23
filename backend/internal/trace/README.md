# trace

Read projections over the EPCIS event store, plus the checks that read them. The module
maintains the current state of every serial (status, disposition, location, lot, expiry,
parent) and the aggregation tree, serves serial history and parent/child lookups, reconciles
lots (mass balance and chain-of-custody gaps), runs pharma lot recalls and signs
tamper-evidence checkpoints. Projection never moves a serial back in time: a row changes only
when the incoming event is not older than its last event. `FinalStates` computes the same end
state from a list of events (used by `migration` shadow runs).

## Endpoints (`/api/v1/trace`, roles admin / serialization / ops / compliance / integration; Viewer reads with GET)

| Method | Path | Purpose |
|---|---|---|
| GET | `/trace/serials/*` | State, ancestors, direct children and event history for an identifier |
| GET | `/trace/tree/*` | Aggregation tree below an identifier (up to 6 levels) |
| GET | `/trace/stats` | Event and capture counts |
| GET | `/trace/state` | List serial state, filtered by `gtin`, `status`, `lot` |
| GET | `/trace/balance?gtin=&lot=` | Mass balance per lot (read-only; lot optional) |
| GET | `/trace/balance/gaps?gtin=&lot=&minSeverity=` | Chain-of-custody gaps with severity (read-only) |
| POST | `/trace/balance/run` | Reconcile, store the report, raise or resolve alerts (Admin, Compliance; audited) |
| GET | `/trace/balance/reports` | Stored runs |
| GET, PUT | `/trace/balance/settings` | Tolerance (units and percent), case proposal, automatic re-run, checkpoint interval (PUT: Admin, Compliance; audited) |
| GET | `/trace/recalls`, `/trace/recalls/{id}` | Lot recalls with holders |
| POST | `/trace/recalls` | Start a recall for a lot: trace forward, notify network partners, mark packs `recalled` (Admin, Compliance; audited) |
| POST | `/trace/recalls/{id}/refresh`, `/notify`, `/close` | Re-trace, re-send to network partners, close |
| POST | `/trace/recalls/{id}/holders/{holder}/sent`, `/acknowledge` | Record a notice as sent / acknowledged |
| GET | `/trace/recalls/{id}/report?format=json\|html` | Recall report export |
| GET | `/trace/integrity/node` | Node key id and public key |
| GET, POST | `/trace/integrity/checkpoints` | List / sign a checkpoint now (POST: Admin, Compliance; audited) |
| GET | `/trace/integrity/checkpoints/{seq}?evidence=1` | Export a checkpoint, optionally with the audit slice and event leaves |
| GET | `/trace/integrity/verify?last=` | Check the live database against the signed checkpoints |

## Background work

- Job `trace.project` (enqueued by `epcis` in every capture transaction): applies the
  capture's events to `serial_state`, marks the capture completed, then publishes the bus event `epcis.captured`.
- Schedule `trace.checkpoint` (hourly): signs a checkpoint for each tenant whose last one is older
  than its interval (default 24 h, `checkpointHours`; 0 turns it off) when the audit chain or the
  events changed.
- Schedule `trace.balance` (every 6 h): re-runs the reconciliation for tenants with `balanceAuto`
  for lots whose serials changed since the lot's last stored run (50 lots per tenant and pass).

## Mass balance

Per serial, from the stored events (repeated copies of the same event count once): inflow =
commissioning + receiving, outflow = shipping + decommissioning + destroying (the business step
decides; without one, an ObjectEvent DELETE reads as decommissioning and a `destroyed`
disposition as destroying). A serial is in order when inflow minus outflow is 0 (gone) or 1
(present); every unit outside that range is a discrepancy. A lot closes when its discrepancy is
at most the larger of `toleranceUnits` and `tolerancePercent` of the packs that entered custody.
Events on a pallet or case count for the packs aggregated into it at the event time. The report
separates data (counts from stored events) from helper statements (balance, tolerance, severity).
A run that does not close raises the alert `trace.balance_discrepancy` (deduplicated per lot, resolved
again when the lot closes); with `proposeCase` the alert detail carries a ready request body for
`POST /api/v1/investigations`. Nothing is opened automatically.

## Chain of custody

A serial seen at a new business location without a shipping event out of the old one or a receiving
event at the new one is a gap (`no_link`, high). Also reported: `shipped_not_received` and
`destination_mismatch` and `seen_after_end_of_life` (medium), `received_without_shipment` (low,
normal when the sender shares no events). Medium and high gaps raise `trace.custody_gap`.

## Lot recall

Reuses the food recall pattern (alert on start, packs marked `recalled`, partners on this node get a
`recall.received` alert) with its own tables. Holders come from the last shipping destination or the
location of a later receiving event; the tenant's own sites count as own stock. Holder names come
from `partner` rows, then the published `directory_listing`. Notice status per holder: `pending`,
`sent`, `acknowledged`; people record the sent and acknowledged steps for holders that are not on this
node. Response-time rules are not implemented (`TODO(spec)`, see `docs/OPEN_QUESTIONS.md`).

## Checkpoints

`internal/core/checkpoint` defines the signed document. A checkpoint covers the audit chain head and the
Merkle root of the events recorded since the previous checkpoint (batches end 30 s before signing so
events still committing are not skipped). Rows are append-only (trigger). Offline check:
`openspoor verify-checkpoint --pubkey <key> checkpoint-N-evidence.json ...`.

## Data

`serial_state` (hash-partitioned into 8 partitions, migration `00003_epcis.sql`). Reads
`epcis_event`, `site`, `partner`, `directory_listing`, `audit_entry`, `federation_node` (the node key)
and updates `epcis_capture` status and `serial_state` (recall); the first group belongs to other modules.
Own tables (migration `00030_trace_integrity.sql`): `trace_setting`, `balance_report`, `lot_recall`,
`lot_recall_holder`, `integrity_checkpoint`. Alerts are written to `alert`.

## Depends on

`core/*` (including `core/checkpoint`), `pkg/epcis`, `pkg/gs1`.

## Spec gaps

`TODO(spec)`: acceptable loss and reporting rules for the mass balance, recall response-time rules and the
authority report format, RFC 3161 timestamps for checkpoints. See `docs/OPEN_QUESTIONS.md`.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/trace`. Unit tests cover the
classification, aggregation attribution, balance, gap and holder logic; integration tests cover the
endpoints, alerts, audit entries, tenant isolation, recall tracking, checkpoints and tampering.
