# Open questions and spec TODOs

Every `TODO(spec)` in the code is listed here. We never invent regulatory requirements: where a specification is
distributed only to onboarded parties, or we could not verify the current version, the code says so and this table tracks it.

## Regulatory specifications

| Area | Question | Where |
|---|---|---|
| EU FMD | The EU Hub interface specification is distributed by EMVO to onboarded OBPs. The connector speaks a documented, minimal JSON shape against the mock hub; map it to the real EMVS messages during onboarding. The real EU Hub URL also comes from EMVO onboarding. | internal/connectors/eufmd |
| Saudi RSD | RSD exposes SOAP web services documented in SFDA's integration guide. The connector models the operations; the exact WSDL version still needs confirming. The rule pack needs exact document versions and section numbers. | internal/connectors/sarsd, rulepacks/sa-rsd.yaml |
| UAE Tatmeen | The Tatmeen interface specification is distributed to onboarded parties. The connector speaks the placeholder JSON shape of `hubjson` against the mock hub. Open: base URL, authentication scheme, participant identifier, report/message types and field names, error codes, and whether and how aggregation (parent/child) is reported. | internal/connectors/aetatmeen, internal/connectors/hubjson |
| UAE Tatmeen rule pack | Only the GS1 AI (21) serial length is evaluated. Cite the governing ministerial decree text before adding rules for 2D carrier content, party identification (GLN), master data registration, reportable events and aggregation. The MOHAP launch notice is listed as a source but could not be fetched for verification. | rulepacks/ae-tatmeen.yaml |
| Egypt EDA | The EDA reporting interface specification is distributed to onboarded parties. Same open points as Tatmeen: base URL, authentication, participant identifier, message types, field names, error codes, aggregation reporting. | internal/connectors/egeda, internal/connectors/hubjson |
| Egypt EDA rule pack | Secondary sources refer to EDA Decrees 161/2025 (unified coding) and 475/2025 (timelines, reporting, penalties). Verify against the official texts and cite them before adding rules for carrier content, timelines, reportable events and aggregation. | rulepacks/eg-eda.yaml |
| DSCSA | Align the EPCIS header vocabulary attribute URIs exactly with the current GS1 US implementation guideline. | internal/dscsa/dscsa.go |
| DSCSA ATP | Authorized trading partner credentials (ADR-010) attest the license recorded in the tenant's license register. Which license or registration types make each kind of trading partner "authorized", and whether a credential should expire before the license, are not settled. | internal/link/atp.go |
| Onboarding templates | The DSCSA wholesaler and EU FMD pharmacy onboarding checklists are practical integration steps, not a statement of regulatory requirements; confirm the items against the applicable guidance. | internal/partners/onboarding.go |
| VRS | Field names follow the GS1 Lightweight Messaging Standard for Verification as published. Confirm against the current revision and the Verification Router Service operators' profiles. | internal/dscsa/vrs.go |
| DSCSA investigations | Response windows are per-tenant settings with unverified defaults (tracing request 48 h, illegitimate product notification 24 h after determination, suspect product none). Confirm each against FD&C Act 582, including whether business days apply. | internal/dscsa/investigation.go |
| DSCSA notifications | The form, channel and required content for notifying FDA and trading partners of illegitimate product, and for terminating a notification after consultation, are not modelled; findings only record that the step happened. | internal/dscsa/investigation.go |
| DSCSA tracing answer | Confirm the content and format a tracing response must take for FDA, other officials and trading partners; the answer carries stored TI/TS and event history for human review. | internal/dscsa/investigation.go |
| FSMA 204 | Align header names and column order with FDA's published sortable-spreadsheet template. | internal/food/food.go |
| EUDR | Map the due-diligence statement payload to the EU Information System (TRACES) DDS schema; submission is stubbed and the reference number is local. | internal/food/recall.go |
| EU General Food Law | Regulation (EC) No 178/2002 Article 18 requires one-step-back/one-step-forward traceability and availability to competent authorities on demand, but prescribes no file format or columns: the one-up/one-down CSV/JSON layout is our own and the export says so. The Commission's guidance on Articles 11, 12, 14, 17, 18, 19 and 20 recommends further information (such as supplier and customer addresses) that CTEs do not capture; confirm the current guidance and decide whether to add addresses from partner master data. Food of animal origin has specific requirements in Implementing Regulation (EU) No 931/2011 that are not modelled. | internal/food/oneup.go |
| Halal / origin certificates | Certificates (including GCC halal) are modelled generically (kind, issuer, number, scope, validity, document); no certification-body or GCC accreditation rules are encoded. Confirm which bodies and accreditation checks, if any, a market requires before validating issuers. | internal/food/certs.go |
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
| SFTP | Inbound EDI files are parsed with `pkg/edi`, which reads X12 850 and EDIFACT ORDERS only; other transaction sets fail with an alert. Outbound drops are EPCIS only (no EDI rendering per partner yet). Delivery is confirmed by the upload; partner pickup is not observable over SFTP. | internal/connectors/sftp.go |
| AS2 | AS2 (RFC 4130) is an interface; the built-in transport refuses with a clear error. Choose a gateway (for example an existing AS2 server) or a vetted Go implementation. | ADR-007, internal/dscsa |
| Federation | Mutual TLS fingerprint pinning is stored but not enforced in-process; the Verifiable Credential binding organisations to nodes is not built (license credentials are, see ADR-010); `did:key` is not supported yet. | ADR-008 |
| GDPR vs GxP | Tenant deletion crypto-shreds the per-tenant data key and deletes events, serial state and the tenant row. The append-only audit chain is kept intact, including its before/after snapshots, because the immutability trigger forbids edits. Decide the retention period per market, and whether audit snapshots containing personal data must be pseudonymised at write time to satisfy erasure requests. | internal/saas/gdpr.go |
| Archive | Captures older than `OPENSPOOR_ARCHIVE_AFTER_DAYS` are copied to S3 as gzipped EPCIS 2.0 files; events also stay in PostgreSQL. Moving cold events out of PostgreSQL (the larger-tier plan) is not built: decide when retention or volume calls for it. | internal/epcis/archive.go |
| Billing | Usage metering is exported to Lago when `OPENSPOOR_LAGO_URL` is set. Stripe is not integrated directly; decide whether Lago's Stripe integration is sufficient. | internal/saas/billing.go |
| Capture latency | The 1k-event capture p95 budget (< 200 ms) holds up to 2 concurrent clients on 4 vCPU (163 ms) and is exceeded at 4 clients (234 ms). Options: more cores, or a separate capture node per tenant group. | bench-results/ |
