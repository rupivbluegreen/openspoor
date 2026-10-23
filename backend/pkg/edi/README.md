# pkg/edi

Maps Open Spoor's canonical B2B `Document` to and from ANSI ASC X12, UN/EDIFACT and IDoc
(XML). Only public segment structures are used. Standard library only. Used by
`internal/link`.

## Document types

| Canonical type | X12 set | EDIFACT message | IDoc basic / message type |
|---|---|---|---|
| `order` | 850 | ORDERS | ORDERS05 / ORDERS |
| `asn` | 856 | DESADV | DELVRY07 / DESADV |
| `invoice` | 810 | INVOIC | INVOIC02 / INVOIC |
| `inventory` | 846 | INVRPT | WMMBID02 / WMMBXY |
| `return` | 180 | RETANN | ORDERS05 / ORDRSP |
| `chargeback` | 844 | COMDIS | INVOIC02 / INVOIC |

## API

| Function | Purpose |
|---|---|
| `X12(doc, env)` | Render an X12 004010 interchange with one transaction set |
| `ParseX12(s)` | Read an X12 850 purchase order |
| `EDIFACT(doc, env)` | Render a UN/EDIFACT D.96A interchange with one message |
| `ParseEDIFACT(s)` | Read an EDIFACT ORDERS message |
| `IDoc(doc, env)` | Render IDoc XML (control record EDI_DC40 plus header, partner and item segments) |
| `X12Set`, `EDIFACTType`, `IDocType` | Look up the identifiers in the table above |
| `Document.Validate`, `Document.Total` | Required-field check, sum of line amounts |

`Envelope` carries sender, receiver and control numbers. Parsing covers orders only; IDoc is
write-only.

## Spec gaps

None marked `TODO(spec)`.

## Tests

From `backend/`: `go test ./pkg/edi` (no database).
