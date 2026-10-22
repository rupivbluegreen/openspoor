# Open questions and spec TODOs

Every `TODO(spec)` in the code is listed here. We never invent regulatory requirements: where a specification is
distributed only to onboarded parties, or we could not verify the current version, the code says so and this table tracks it.

## Regulatory specifications

| Area | Question | Where |
|---|---|---|
| EU FMD | The EU Hub interface specification is distributed by EMVO to onboarded OBPs. The connector speaks a documented, minimal JSON shape against the mock hub; map it to the real EMVS messages during onboarding. The real EU Hub URL also comes from EMVO onboarding. | internal/connectors/eufmd |
| Saudi RSD | RSD exposes SOAP web services documented in SFDA's integration guide. The connector models the operations; the exact WSDL version still needs confirming. The rule pack needs exact document versions and section numbers. | internal/connectors/sarsd, rulepacks/sa-rsd.yaml |
| DSCSA | Align the EPCIS header vocabulary attribute URIs exactly with the current GS1 US implementation guideline. | internal/dscsa/dscsa.go |
| VRS | Field names follow the GS1 Lightweight Messaging Standard for Verification as published. Confirm against the current revision and the Verification Router Service operators' profiles. | internal/dscsa/vrs.go |
| FSMA 204 | Align header names and column order with FDA's published sortable-spreadsheet template. | internal/food/food.go |
| EUDR | Map the due-diligence statement payload to the EU Information System (TRACES) DDS schema; submission is stubbed and the reference number is local. | internal/food/recall.go |
| UDI | GUDID takes HL7 SPL XML through the FDA Electronic Submissions Gateway, and EUDAMED M2M uses the Commission's XSD-defined XML. Payloads are prepared locally; transmission needs gateway onboarding. | internal/ecosystem/udi.go |

## Standards conformance

| Area | Question | Where |
|---|---|---|
| EPCIS | Event hash IDs follow the structure of the CBV 2.0 event hash but are not yet byte-identical to the GS1 canonical algorithm, so they are tagged `ver=openspoor1`. | pkg/epcis/hash.go |
| Serialization | DataMatrix preview images lack FNC1 in the first position (an encoder limitation). Production printing comes from Level 2 line systems. | internal/serialization |

## Architecture and operations

| Area | Question | Where |
|---|---|---|
| Workflows | Temporal is in the compose stack (ADR-004), but long-running human workflows (recall, batch release) and connector retries run on the PostgreSQL job queue. Wire Temporal workers when a workflow needs durable timers measured in weeks. | ADR-004, internal/act/workflows.go |
| AS2 | AS2 (RFC 4130) is an interface; the built-in transport refuses with a clear error. Choose a gateway (for example an existing AS2 server) or a vetted Go implementation. | ADR-007, internal/dscsa |
| Federation | Mutual TLS fingerprint pinning is stored but not enforced in-process; the Verifiable Credential binding organisations to nodes is not built; `did:key` is not supported yet. | ADR-008 |
| GDPR vs GxP | Tenant deletion crypto-shreds the per-tenant data key and deletes events, serial state and the tenant row. The append-only audit chain is kept intact, including its before/after snapshots, because the immutability trigger forbids edits. Decide the retention period per market, and whether audit snapshots containing personal data must be pseudonymised at write time to satisfy erasure requests. | internal/saas/gdpr.go |
| Archive | Raw capture documents are not yet archived to S3 (SeaweedFS, ADR-005); `OPENSPOOR_S3_ENDPOINT` is read but unused. Events themselves are stored in PostgreSQL. | ADR-005 |
| Billing | Usage metering is exported to Lago when `OPENSPOOR_LAGO_URL` is set. Stripe is not integrated directly; decide whether Lago's Stripe integration is sufficient. | internal/saas/billing.go |
| Capture latency | The 1k-event capture p95 budget (< 200 ms) holds up to 2 concurrent clients on 4 vCPU (163 ms) and is exceeded at 4 clients (234 ms). Options: more cores, or a separate capture node per tenant group. | bench-results/ |
