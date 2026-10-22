# Open questions and spec TODOs

Items marked `TODO(spec)` in code are listed here. We never invent regulatory requirements.

| Area | Question | Where |
|---|---|---|
| EPCIS | Event hash IDs follow the CBV 2.0 hash structure but are not yet byte-identical to the GS1 canonical algorithm (tagged `ver=openspoor1`). | pkg/epcis/hash.go |
| Serialization | DataMatrix preview images lack FNC1 in the first position (encoder limitation); production printing comes from Level 2 printers. | internal/serialization |
| Workflows | Wire Temporal workers for long-running human workflows (recall, batch release); connector retries currently use the PostgreSQL job queue. | ADR-004 |
