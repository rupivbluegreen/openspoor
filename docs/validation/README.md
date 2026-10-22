# Validation pack (GAMP 5, risk-based)

Templates to qualify an Open Spoor installation as a computerized system. They are starting points: your quality unit owns the final documents.

| Document | File | Produced by |
|---|---|---|
| User Requirements Specification (URS) | `templates/URS.md` | you (adapt) |
| Risk assessment (FMEA) | `templates/RiskAssessment.md` | you (adapt) |
| Installation Qualification (IQ) | `templates/IQ.md` | executed per installation |
| Operational Qualification (OQ) | generated: `OQ-report.md` | `make -C backend validation-pack` from the automated test run |
| Traceability matrix | `traceability.yaml` | maintained with the code: URS IDs ↔ automated tests |

The OQ report is generated from the actual `go test -json` results of the release, with the commit, environment and timestamp, and maps every requirement to the tests that verify it. A requirement without a passing test is listed as **not verified**.
