# link

B2B exchange between trading partners. The module owns the B2B transaction set (orders,
ASN, invoices, inventory reports, returns, chargebacks) with X12, EDIFACT and IDoc renderings
via `pkg/edi`, the supplier compliance hub (certificates, audits, specifications, with expiry
alerts), document exchange between partner tenants, and node-to-node federation (ADR-008).

Federation: each node has an Ed25519 identity published as a DID document. Peers are added
and approved explicitly; messages (`ping`, `epcis.capture`, `b2b.document`) travel as signed
envelopes, and inbound envelopes are rejected on a bad signature or more than 5 minutes of
clock skew. Inbound `epcis.capture` payloads are captured into the EPCIS repository.

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| GET, POST | `/api/v1/b2b/documents` | List / create B2B documents |
| POST | `/api/v1/b2b/documents/import` | Parse an inbound X12 850 or EDIFACT ORDERS message |
| GET | `/api/v1/b2b/documents/{id}` | Get a document |
| GET | `/api/v1/b2b/documents/{id}/edi` | Render as X12, EDIFACT or IDoc |
| POST | `/api/v1/b2b/documents/{id}/send` | Deliver to a partner on this node, over federation, or mark sent for EDI pickup |
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

Authenticated routes need the admin, ops, compliance or integration role.

## Background work

- Job `federation.deliver`: delivers one signed envelope, with retries.
- Schedule `link.cert_expiry` (every 6 h): alerts on certificates expiring within 30 days.

## Data

`b2b_document`, `supplier_record`, `shared_document`, `federation_node`, `federation_peer`,
`federation_message` (migration `00008_link.sql`).

## Depends on

`core/*`, `epcis` (federated captures), `partners` (partner lookup), `pkg/edi`.

## Spec gaps

None marked `TODO(spec)`.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/link`.
