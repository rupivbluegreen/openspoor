---
title: EPCIS 2.0 in ten minutes
tags: [trace, epcis, basics]
---
EPCIS (Electronic Product Code Information Services) is the GS1 standard for sharing **what happened to which object, when, where and why**. Every record is an *event*:

| Dimension | Field | Example |
|---|---|---|
| What | `epcList`, `parentID`, `childEPCs`, `quantityList` | `urn:epc:id:sgtin:9506000.013435.A1B2` or a GS1 Digital Link |
| When | `eventTime`, `eventTimeZoneOffset` | `2026-03-01T09:00:00+01:00` |
| Where | `readPoint`, `bizLocation` | an SGLN / GLN |
| Why | `bizStep`, `disposition`, `bizTransactionList` | `shipping`, `in_transit`, a PO number |

Five event types cover the supply chain: **ObjectEvent** (commission, observe, decommission), **AggregationEvent** (pack units into cases and pallets), **TransactionEvent** (link objects to business documents), **TransformationEvent** (inputs become outputs — food and APIs) and **AssociationEvent** (attach a sensor or asset).

Values for `bizStep` and `disposition` come from the GS1 **Core Business Vocabulary (CBV)**. Open Spoor accepts the bare form (`shipping`), the URN form (`urn:epcglobal:cbv:bizstep:shipping`) and the Web URI form, and stores them normalized.

In Open Spoor: capture at `POST /api/v1/epcis/capture` (JSON-LD or XML, 1.2 accepted), query at `GET /api/v1/epcis/events`, and try documents without an account in the **Playground**.
