# Risk assessment (FMEA) — Open Spoor

Scale 1–5 for Severity (S), Occurrence (O), Detectability (D); RPN = S×O×D. Controls reference built-in features and their verifying tests.

| ID | Failure mode | Effect | S | O | D | RPN | Controls | Residual |
|---|---|---|---|---|---|---|---|---|
| R-01 | Cross-tenant data access | Confidentiality breach | 5 | 2 | 2 | 20 | Forced PostgreSQL RLS, non-superuser role, isolation tests (URS-01) | Low |
| R-02 | Audit trail altered | Loss of data integrity | 5 | 1 | 1 | 5 | Append-only trigger, hash chain, `/audit/verify` (URS-02) | Low |
| R-03 | Duplicate serial issued | Pack cannot be verified, possible recall | 4 | 1 | 2 | 8 | FF1 permutation, monotonic counter, idempotency keys (URS-04) | Low |
| R-04 | Invalid EPCIS silently stored | Wrong regulatory reporting | 4 | 2 | 2 | 16 | Atomic capture with per-event errors (URS-06) | Low |
| R-05 | Hub rejection unnoticed | Non-compliance | 4 | 3 | 1 | 12 | Alert inbox, control tower monitor, exception-triage agent (URS-10) | Low |
| R-06 | Agent acts without oversight | Unauthorized change | 4 | 2 | 1 | 8 | Proposal-only by default, role scopes, e-signature for high risk, revert (URS-12) | Low |
| R-07 | Loss of master key | Sealed secrets unrecoverable | 4 | 1 | 3 | 12 | Secret-store custody, backup runbook | Medium — procedural |
| R-08 | Performance degradation | Late acknowledgements | 3 | 2 | 1 | 6 | k6 budgets, Prometheus histograms (URS-09, URS-15) | Low |
