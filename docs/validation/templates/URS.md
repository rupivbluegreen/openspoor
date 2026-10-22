# User Requirements Specification — Open Spoor

| ID | Requirement | GxP | Verified by (see traceability.yaml) |
|---|---|---|---|
| URS-01 | Each tenant's data is isolated; no user can read or write another tenant's records. | yes | OQ |
| URS-02 | Every create/update/delete is recorded in an append-only, tamper-evident audit trail (who, what, when, before/after, reason). | yes | OQ |
| URS-03 | Electronic signatures require re-authentication and record meaning and reason (21 CFR Part 11 §11.50, §11.200). | yes | OQ |
| URS-04 | Serial numbers are unique, never reissued, and allocation is idempotent. | yes | OQ |
| URS-05 | Random serial numbers are not predictable (EU FMD 2016/161 Art. 4(b)). | yes | OQ |
| URS-06 | EPCIS documents are validated; invalid documents are rejected with per-event errors and nothing is stored. | yes | OQ |
| URS-07 | Serial history and aggregation hierarchy can be retrieved for any identifier. | yes | OQ |
| URS-08 | DSCSA TI/TS is produced for each shipment and reconciled against the ASN on receipt. | yes | OQ |
| URS-09 | Product verification (VRS) answers with the manufacturer's record within 1 s (p95). | yes | OQ + performance |
| URS-10 | Regulatory submissions are retried, rejections raise alerts with the reason and a suggested fix, and can be resubmitted. | yes | OQ |
| URS-11 | Recalls identify all affected lots (including derived lots) and customers. | yes | OQ |
| URS-12 | Agent actions require human approval (e-signature for high risk) and can be reverted. | yes | OQ |
| URS-13 | Records are retained for the regulatory period (DSCSA: 6 years) and cannot be shortened below it. | yes | OQ |
| URS-14 | Tenant data can be exported and erased on request (GDPR Art. 17, 20). | no | OQ |
| URS-15 | EPCIS capture acknowledges a 1,000-event document within 200 ms (p95). | no | performance |
