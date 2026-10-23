# pkg/gs1

GS1 identifiers from the public GS1 General Specifications, GS1 Digital Link URI syntax and
the EPC Tag Data Standard (pure identity URNs). Standard library only.

## What it covers

| Area | Functions |
|---|---|
| Check digits and keys | `CheckDigit`, `WithCheckDigit`, `ValidGTIN`, `ValidGLN`, `ValidSSCC`, `NormalizeGTIN` |
| Element strings | `ParseElementString` (GS-separated, optional `]d2` prefix, or `(01)...(21)...` HRI form), `ElementString`, `HRI` |
| Packs | `Pack` (GTIN, serial, lot, expiry, NHRN), `PackFromAIs`, `ExpiryDate` (YYMMDD; day `00` means end of month) |
| Digital Link | `DigitalLink`, `ParseDigitalLink`, `IsDigitalLink`, `DefaultResolver` (`https://id.gs1.org`) |
| EPC URNs | `SGTINURN` / `ParseSGTINURN`, `LGTINURN`, `SSCCURN` / `ParseSSCCURN`, `SGLNURN` / `ParseSGLNURN` |
| Conversion | `URNToDigitalLink`, `DigitalLinkToURN` (needs the company prefix length) |

Known AIs: 00, 01, 02, 10, 11, 12, 13, 15, 16, 17, 20, 21, 22, 235, 240, 241, 250, 254, 400, 401,
410, 411, 412, 414, 416, 710–714, 7003 and 7240.

## Used by

`pkg/epcis`, `serialization`, `masterdata`, `epcis`, `verify` and most other modules.

## Spec gaps

None marked `TODO(spec)`.

## Tests

From `backend/`: `go test ./pkg/gs1` (no database).
