# link

B2B exchange between trading partners. The module owns the B2B transaction set (60 types in the
`pkg/edi` registry: forecasts, orders and their acknowledgements and changes, ASN, receiving
advices, invoices, remittances, inventory and sales reports, returns, chargebacks, claims,
acknowledgements, master data) with X12, EDIFACT and IDoc renderings where the type has a writer
(`GET /b2b/types` says which), the supplier compliance hub (certificates, audits, specifications, with expiry
alerts), document exchange between partner tenants, and node-to-node federation (ADR-008).

Federation: each node has an Ed25519 identity published as a DID document. Peers are added
and approved explicitly; messages (`ping`, `epcis.capture`, `b2b.document`) travel as signed
envelopes, and inbound envelopes are rejected on a bad signature or more than 5 minutes of
clock skew. Inbound `epcis.capture` payloads are captured into the EPCIS repository.
Outbound envelopes are re-signed with a fresh `createdAt` on every delivery attempt (same id).

Transaction types: every document has a registry type (`pkg/edi`) that defines its required
fields, allowed statuses, dates, parties and amounts, and validation rules. Creating, importing,
mapping and receiving a document validate it against its type. The registry is listed at
`GET /b2b/types` (key, name, category, flow, X12 and EDIFACT identifiers, required fields,
rules, available renderings, mapping target fields). Rendering a document in a format its type has
no writer for is a 400. X12 import reads the 850 and every type with an X12 template; EDIFACT
import reads any registry message, and an optional `type` names the type where several share a
message and document name code (ORDRSP: order acknowledgement and change acknowledgement).

Mapping studio: saved mappings per partner and document type turn CSV, JSON (records and
nested lines by dotted path), XML, X12, EDIFACT or fixed-width files into canonical B2B documents
(`pkg/edi`) or EPCIS events. X12 and EDIFACT files are read with the `pkg/edi` segment reader
(`ParseSegments`) and XML with `encoding/xml`; every element becomes a source field with a path:
`SEG.NN` (element NN of the first occurrence of a segment, `BEG.03`), `SEG.NN.MM` (component MM of a
composite, `DTM.01.02`; the whole element is `SEG.NN`), `SEG[k].NN` (k-th occurrence, k >= 2) and
`SEG(Q).NN` (first occurrence of the segment whose first element is the qualifier Q, `DTM(137).01.02`,
`N1(BY).02`; offered for DTM, REF, N1, N9, NAD, RFF, QTY, MOA, PIA, FTX, GIN, NTE, LOC, AMT, PER, CTA,
COM, TAX only). Each transaction set or message is one record; its lines start at the segment named by
`options.linesPath` (a segment ID such as `PO1` or `LIN`) or, if empty, at the first of PO1, IT1, POC,
LIN ... (X12) or LIN (EDIFACT) the message contains, and end at a summary segment (CTT, UNS, CNT ...);
envelope segments (ISA, GS, UNB) are available in every record. For XML the root element is not part
of a path: children are addressed by local name (namespaces ignored), attributes as `@name`, the text
of an element that has attributes or children as `#text`, repeated siblings by index (`Line.0.Qty`);
`recordsPath` and `linesPath` work as for JSON and a single child counts as one line. Limits: XML
nesting 64 levels and 1,000,000 elements, 500,000 segments per file, UTF-8 or ISO-8859-1 only;
DOCTYPE entities are not expanded. A file that cannot be read at all (not an interchange, a
truncated ISA, malformed XML, an X12 file under an EDIFACT mapping) is a 400 and nothing is stored. Transforms: `trim`, `upper`, `lower`, `date` (YYYY/MM/DD/HH/mm/ss patterns),
`lookup` (table with optional default), `constant`, `concat`. Rows are grouped into documents by
the mapped `number`. A preview returns the canonical result with per-row errors; inbound import
(`format` csv, json, xml, flat or mapped, or x12 / edifact together with a `mappingId`) is
all-or-nothing. X12 and EDIFACT without a `mappingId` are parsed natively into the registry type. "Suggest" proposes fields from header
names (a heuristic; a person reviews and saves); for X12 and EDIFACT paths it uses the usual segment
positions (`BEG.03`, `BGM.02`, `PO1.07`, `LIN.03.01`, `N1(BY).04`, `NAD(BY).02.01` ...) and adds a
`YYYYMMDD` date transform to date fields. The target field list of a mapping comes from
the registry type: header fields, `status`, `note`, `dates.<key>`, `parties.<key>.name|gln`,
`amounts.<key>`, and line fields (`lines.gtin`... for item lines, `lines.ref|refType|amount|status`
for reference lines, `lines.dates.<key>`).

Message tracking: `/messages` is one log over B2B documents, federation messages, TI/TS
deliveries (read from the `dscsa` shipment table; attempts count every send, a delivery whose
AS2 receipt was verified shows as `acknowledged`) and AS2 messages (`kind=as2`, read from
`as2_message`). TI/TS rows are retryable when the delivery failed: the retry is synchronous, runs
the dscsa outbound path again (wired as `Module.RetryShipment`; link does not import dscsa), is
audited as `message.retry` and resolves the shipment's `dscsa.send_failed` alerts on success.
A retry holds the shipment in `sending`; a claim older than five minutes counts as crashed and
can be taken over. Attempts, last error and the dead-letter state
come from the job queue: a delivery job that exhausted its retries (or was rejected) shows as
`dead`, raises a `link.message_dead` alert, and can be put back with "retry now". A document sent to a partner
that has no linked tenant and no federation endpoint is dropped as an X12 or EDIFACT file on the
partner's SFTP server when an enabled SFTP connector instance is bound to the partner (`partnerId` and
`documentDir` in its configuration; `documentFormat` x12, edifact or auto); otherwise it is marked sent for
EDI pickup. The drop is logged in `sftp_file` (direction outbound), audited (`sftp.outbound.processed`
or `.failed`, plus `b2b.send`) and a failure is a 502 with an alert; the document stays a draft.
Outbound B2B
documents are acknowledged by local delivery, by the peer accepting a federation message, or by
`POST /b2b/documents/{id}/ack` (functional acknowledgement from an EDI gateway). Partner SLAs
set an acknowledgement window; breaches raise `link.sla_breach` alerts.

Onboarding test harness: sends a test over the partner's channel (local connection check,
signed federation ping delivered immediately, or an empty EPCIS document to an HTTP capture
endpoint) and records the run on the partner's onboarding (`partners.RecordTest`).

Authorized trading partner credentials (ADR-010): W3C VC 2.0 credentials for a license in the
tenant's license register, signed by the node key with an `eddsa-jcs-2022` Data Integrity proof,
revocable through a W3C Bitstring Status List served publicly. Presented credentials are
verified (did:web resolution, proof, validity, revocation) and the result is stored per partner.

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/v1/b2b/types` | The transaction type registry (filters: `category`, `q`) |
| GET, POST | `/api/v1/b2b/documents` | List / create B2B documents of any registry type (filter: `type`, `direction`) |
| POST | `/api/v1/b2b/documents/import` | Parse an inbound X12 or EDIFACT message of a registry type (or map a CSV, JSON, XML, X12, EDIFACT or fixed-width file with a saved mapping) |
| GET | `/api/v1/b2b/documents/{id}` | Get a document |
| GET | `/api/v1/b2b/documents/{id}/edi` | Render as X12, EDIFACT or IDoc |
| POST | `/api/v1/b2b/documents/{id}/send` | Deliver to a partner on this node, over federation, as a file on the partner's SFTP server, or mark sent for EDI pickup |
| GET, POST | `/api/v1/suppliers/certificates` | List / add supplier certificates, audits, specs |
| GET, POST | `/api/v1/documents` | List / upload shared documents |
| GET | `/api/v1/documents/{id}` | Download a document |
| POST | `/api/v1/documents/{id}/share` | Send a copy to a partner tenant on this node |
| GET | `/api/v1/federation/node` | This node's identity |
| GET, POST | `/api/v1/federation/peers` | List / add peers |
| POST | `/api/v1/federation/peers/{id}/approve` | Approve a peer |
| POST | `/api/v1/federation/send` | Send a signed message to a peer |
| GET | `/api/v1/federation/messages` | Message log |
| POST | `/api/v1/federation/inbox` | Receive signed messages from peers (public) |
| GET | `/.well-known/did.json` | Node DID document (public) |
| GET, POST | `/api/v1/b2b/mappings` | List / create mappings |
| GET, PUT, DELETE | `/api/v1/b2b/mappings/{id}` | Read / update / delete a mapping |
| POST | `/api/v1/b2b/mappings/preview` | Apply a saved or inline mapping to a sample file |
| POST | `/api/v1/b2b/mappings/suggest` | Propose field mappings from column names |
| POST | `/api/v1/b2b/documents/{id}/ack` | Record a functional acknowledgement |
| GET | `/api/v1/messages` | Message log (filters: kind, direction, status, partnerId, q) with counts per status |
| POST | `/api/v1/messages/{id}/retry` | Retry a dead or backing-off delivery now; for a failed outbound TI/TS (`kind=tits`) re-send it through the dscsa outbound path (200 with the new status) |
| GET | `/api/v1/messages/slas` | Partner SLAs |
| PUT, DELETE | `/api/v1/messages/slas/{partnerId}` | Set / remove a partner's SLA |
| POST | `/api/v1/onboarding/tests` | Run the onboarding test harness for a partner |
| GET, POST | `/api/v1/credentials` | List / issue ATP credentials (issue: admin, compliance) |
| GET | `/api/v1/credentials/{id}` | An issued credential |
| POST | `/api/v1/credentials/{id}/revoke` | Revoke (admin, compliance) |
| POST | `/api/v1/credentials/verify` | Verify a presented credential |
| GET | `/api/v1/credentials/verifications` | Verification results (filter: partnerId) |
| GET | `/api/v1/credentials/status/revocation` | Signed revocation status list (public) |

Authenticated routes need the admin, ops, compliance or integration role (Viewer may read).

## Background work

- Job `federation.deliver`: delivers one signed envelope, with retries.
- Schedule `link.cert_expiry` (every 6 h): alerts on certificates expiring within 30 days.
- Schedule `link.sla` (every 15 min): alerts on outbound messages not acknowledged within the partner's SLA.

## Data

`b2b_document`, `supplier_record`, `shared_document`, `federation_node`, `federation_peer`,
`federation_message` (migration `00008_link.sql`; migration `00022_kpi.sql` replaces the fixed type lists
of `b2b_document.type` and `b2b_mapping.doc_type` with a shape check, the registry validates the value); `b2b_mapping`, `partner_sla`, `atp_credential`,
`atp_verification`, `b2b_document.acked_at`, `federation_message.job_id` (migration `00015_link_network.sql`); migration
`00026_mapping_formats.sql` widens the `b2b_mapping.format` check to xml, x12 and edifact.
Reads `license`/`organization`/`site` (platform) for issuance and `shipment` (dscsa) for the message log
inside the tenant's transaction.

## Depends on

`core/*`, `epcis` (federated captures), `partners` (partner lookup), `pkg/edi`.

## Spec gaps

- `TODO(spec)` in `atp.go`: which license types make a party an authorized trading partner under DSCSA.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/link`.
