# pkg

Reusable libraries with no dependency on `internal/`. Each has its own README.

| Package | Purpose | Depends on |
|---|---|---|
| [`gs1`](gs1/README.md) | GS1 keys, element strings, Digital Link, EPC URNs | standard library |
| [`epcis`](epcis/README.md) | EPCIS 2.0 documents: read/write JSON-LD and XML, CBV validation, identifier normalization | `pkg/gs1` |
| [`edi`](edi/README.md) | Canonical B2B documents to and from X12, EDIFACT and IDoc XML | standard library |
| [`appsdk`](appsdk/README.md) | Client helpers for third-party apps: webhook verification, API client, manifest | standard library |

All tests here are unit tests and need no database. From `backend/`:

```sh
go test ./pkg/...
```
