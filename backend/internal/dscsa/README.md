# dscsa

US DSCSA interoperability. The module owns outbound shipments and their Transaction
Information / Transaction Statement (TI/TS) EPCIS documents, partner exchange (HTTPS; AS2
sender and receiver in process, see ADR-007; a `local` transport delivers between tenants on
the same node), reconciliation of inbound TI/TS with the ASN, the
Verification Router Service (VRS) requester and responder (GS1 Lightweight Messaging), a VRS
lookup directory, record retention (six years by default, per-tenant overrides), and
investigations: tracing requests and suspect / illegitimate product investigations.

## Endpoints (`/api/v1`)

| Method | Path | Purpose |
|---|---|---|
| GET, POST | `/shipments` | List / create outbound shipments |
| POST | `/shipments/inbound` | Receive an inbound TI/TS document |
| GET | `/shipments/{id}` | Get a shipment |
| POST | `/shipments/{id}/tits` | Build the TI/TS document |
| POST | `/shipments/{id}/send` | Send TI/TS to the partner |
| GET, PUT | `/as2/station` | This node's AS2 identity (PUT: admin / integration; `generate` makes a self-signed RSA-2048 certificate; the key is sealed and never returned) |
| POST | `/as2/receive` | AS2 receive endpoint: messages and asynchronous receipts (public; the sender is authenticated by its pinned certificate) |
| POST | `/shipments/{id}/reconcile` | Reconcile inbound TI/TS with the ASN |
| GET, POST | `/vrs/requests` | List / send VRS verification requests |
| GET, POST | `/vrs/directory` | List / upsert VRS lookup directory entries |
| DELETE | `/vrs/directory/{id}` | Remove a directory entry |
| GET | `/retention` | Retention policies |
| PUT | `/retention/{class}` | Set a retention period (admin / compliance) |
| POST | `/vrs/verify` | VRS responder (public, no auth) |
| GET, POST | `/investigations` | List (filters `kind`, `status` (or `active`), `gtin`, `overdue=true`, `q`) / open an investigation |
| POST | `/investigations/from-alert` | Open a suspect investigation from a `verify.*` or `dscsa.vrs_not_verified` alert; resolves the alert and links it |
| GET | `/investigations/policies` | Response windows per kind (defaults flagged `isDefault`) |
| PUT | `/investigations/policies/{kind}` | Set a tenant's window in hours (0 = no due date) with its basis |
| GET | `/investigations/{id}` | Investigation, timeline and allowed transitions |
| POST | `/investigations/{id}/transition` | Move through the state machine (`to`, `note`) |
| POST | `/investigations/{id}/findings` | Record a finding (note, evidence, verification, partner/authority notification, consultation, notification_terminated) |
| POST, GET | `/investigations/{id}/tracing-answer` | Assemble (and store) / download the tracing answer JSON |
| POST | `/investigations/{id}/close` | Close with `decision` and, where required, `signatureId` |

Shipment and VRS routes need the admin, ops, compliance or integration role. Investigation
writes need admin or compliance; viewers read.

### AS2

`transport: "as2"` on a shipment sends the TI/TS (`application/ld+json`) as an AS2 message.
The pieces:

- **Station**: this node's AS2 identifier, certificate and private key (`as2_station`, key sealed
  with the tenant key). The AS2 identifier is unique across the node because an inbound message
  is routed by `AS2-To`.
- **Partner profile** (`PUT /partners/{id}/as2`, in the partners module): the partner's
  certificate (sealed), `sign`, `encrypt` (both default to true), `mdn` (`sync` or `async`) and
  `mdnUrl`. The partner record also needs `endpoints.as2Id` and `endpoints.as2Url`.
- **Send**: build, sign and encrypt (`pkg/as2`), record an `as2_message` row, POST, then verify the
  receipt: a signed MDN from the partner, for this Message-ID, with the message integrity check we
  computed and a `processed` disposition. Anything else fails the shipment (`send_failed`, alert).
  With an async profile the shipment stays `sent` and the row `mdn_pending` until the receipt
  arrives at `/as2/receive`; an error receipt then fails the shipment and raises the alert.
- **Receive**: route by `AS2-To` to the tenant, find the partner by `AS2-From`, decrypt, verify the
  signature against the pinned certificate and enforce the connection's `sign` / `encrypt`. The
  Message-ID is claimed (`as2_message` unique key) before processing; a repeat gets the stored
  receipt and is not processed again. The payload goes through the normal inbound TI/TS path
  (`Receive`), the purchase order number being read from the document. A signed MDN is returned
  inline (sync) or queued as job `dscsa.as2.mdn` (async). A message that fails returns an error
  MDN (`authentication-failed`, `integrity-check-failed`, `decryption-failed`,
  `insufficient-message-security`, `unexpected-processing-error`); with no receipt requested it is
  HTTP 400. An unknown sender is HTTP 403 and an unknown `AS2-To` is 404, with no receipt.
- Async receipts are posted only to the receipt URL configured for the connection (`mdnUrl`, else
  the partner's AS2 URL); a message whose `Receipt-Delivery-Option` names another URL is refused.
- Audit actions: `as2.send`, `as2.receive`, `as2.receive.duplicate`, `as2.receive.rejected`,
  `as2.mdn.receive`, `as2.mdn.send`, `as2.station.set` (and `partner.as2.set` / `.delete`).
  Alerts: `dscsa.send_failed`, `dscsa.as2_rejected` (one per sender and reason),
  `dscsa.as2_mdn_failed`.
- A failed TI/TS of any transport can be re-sent with `POST /messages/{id}/retry` (link module):
  it runs the same outbound path again (`Module.Resend`), once at a time per shipment.

Algorithm profile, trust model and open questions: `pkg/as2/README.md` and
`docs/OPEN_QUESTIONS.md`.

### Investigations

State machine: `open → quarantined → investigating → cleared | confirmed_illegitimate → closed`
(`open → investigating` is allowed when nothing is in the company's possession). Tracing
requests are never quarantined and may close from `investigating`, but only after the tracing
answer is assembled. Invalid transitions return 409.

- Scope (GTIN + serials, lots, raw identifiers or a shipment) is resolved against
  `serial_state` at creation, including everything aggregated below; unknown identifiers are
  kept in `notFound`.
- Quarantine captures an EPCIS `ObjectEvent` (OBSERVE, bizStep `holding`, disposition
  `non_sellable_other`) through the epcis module, so the trace module's serial status follows.
  While held, `BuildTITS` refuses to ship the identifiers. Clearing captures a release event
  (bizStep `inspecting`, disposition `sellable_accessible`) for identifiers no other
  investigation still holds. A confirmed illegitimate product stays held; its disposition
  (destruction, return) is a separate EPCIS event.
- `confirmed_illegitimate` turns the investigation into kind `illegitimate` and sets `dueAt`
  from the tenant's illegitimate-product window.
- Closing an investigation of kind `illegitimate` (or from `confirmed_illegitimate`) requires
  the caller's electronic signature: `POST /esignatures` with `entity=investigation`,
  `entityId=<id>` (password re-authentication), given after the investigation's last change,
  then pass `signatureId`. Missing or foreign signatures return 428.
- The tracing answer lists, per identifier, its state, containers and EPCIS history, and per
  shipment carrying it or its containers the transaction information and statement from the
  stored TI/TS (rebuilt with `BuildTITS` when an outbound shipment never had one, flagged
  `rebuilt`). A person reviews and sends it; nothing is transmitted automatically.
- Each write appends to the `investigation_event` timeline and writes an audit entry
  (`investigation.create`, `investigation.<status>`, `investigation.finding`,
  `investigation.tracing_answer`, `investigation.policy.put`, `alert.resolve`).
- A VRS response of "not verified" (not a transport failure) raises a
  `dscsa.vrs_not_verified` alert.

## Background work

- Schedule `dscsa.retention` (every 24 h): deletes EPCIS events and VRS requests older than
  each tenant's retention period (never less than six years).
- Schedule `dscsa.investigations_overdue` (hourly): raises one `dscsa.investigation_overdue`
  alert per open investigation past its due date (deduplicated while the alert is open).

## Data

`shipment`, `vrs_directory`, `vrs_request`, `retention_policy` (migration
`00004_partners_dscsa.sql`); `investigation`, `investigation_event`, `investigation_policy`
(migration `00013_investigations.sql`); `as2_station`, `as2_message` and `partner_as2`, plus
`shipment.send_attempts` (migration `00023_as2.sql`). Outbound and inbound TI/TS are captured into
the EPCIS repository.

## Depends on

`core/*`, `epcis`, `masterdata`, `partners`, `trace`, `pkg/as2`, `pkg/epcis`, `pkg/gs1`.

## Spec gaps

- AS2: algorithm profile, payload shape, trust and revocation, inbound ASN, unsigned-message MIC
  (`TODO(spec)` in `pkg/as2` and `as2.go`; all in `docs/OPEN_QUESTIONS.md`).

- TI/TS header: "align header vocabulary attribute URIs exactly with the current GS1 US
  Healthcare 'Applying GS1 Standards for DSCSA' guideline before production use."
- VRS: "field names follow the GS1 Lightweight Messaging Standard ... confirm against the
  current release before certification."
- Investigation windows: the defaults (tracing request 48 h, illegitimate product 24 h after
  determination, suspect product none) are placeholders, not verified deadlines; confirm them
  against FD&C Act 582 (including whether business days apply) and set them per tenant.
- Notifications: the form, channel and content for notifying FDA and trading partners of
  illegitimate product, and for terminating a notification after consultation, are not
  modelled; findings record that the step happened and its reference.
- Tracing answer: confirm the content and format each requester (FDA, other officials,
  trading partners) expects.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/dscsa`.
