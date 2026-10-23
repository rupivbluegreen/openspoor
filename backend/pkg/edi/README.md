# pkg/edi

Maps Open Spoor's canonical B2B `Document` to and from ANSI ASC X12, UN/EDIFACT and IDoc
(XML). Only public segment structures are used. Standard library only. Used by
`internal/link` and the SFTP connector.

## The registry

`Types` (registry.go, registry_data.go) is a data table with one entry per canonical
transaction type: key, name, category (planning, ordering, fulfilment, invoicing, inventory,
returns and claims, compliance, master data), usual flow (buyer to seller, seller to buyer, either),
the X12 transaction set and the EDIFACT message (with document name code) where a public
equivalent is attributed, the required canonical fields, the allowed status codes, dates,
parties and amounts, and declarative validation rules. Everything else follows from it:
`Document.Validate`, the mapping-studio target list (`Type.Targets`), the API listing
(`Infos`) and the generic X12 and EDIFACT writers and readers.

There are **60 types**: 19 with an X12 writer (the 6 hand-written mappers plus 13 template-driven
ones), 35 with an EDIFACT writer, 14 with an X12 reader (the 850 and the 13 templates), 35 with an
EDIFACT reader (a message is read exactly where it can be written), 6 with an IDoc writer, and the
rest catalogued (identifiers, required fields and validation, canonical JSON, but no rendering
yet). Adding a type is adding an entry; adding an EDIFACT writer needs no code, adding an X12
writer needs a template (a few header segments and a line profile). 65 was not reached: every
entry is a standard-backed type with its own X12 set or EDIFACT message, and those are all that
could be attributed with certainty. The counts are asserted by `TestFormatCounts`.

**planning**

| Key | Name | X12 | EDIFACT (BGM) | Writers |
|---|---|---|---|---|
| `forecast` | Planning schedule / forecast | 830 | DELFOR | X12, EDIFACT |
| `shipping_schedule` | Shipping schedule | 862 | DELJIT | X12, EDIFACT |
| `sales_forecast` | Sales forecast |  | SLSFCT | EDIFACT |

**ordering**

| Key | Name | X12 | EDIFACT (BGM) | Writers |
|---|---|---|---|---|
| `order` | Purchase order | 850 | ORDERS (220) | X12, EDIFACT, IDoc |
| `order_ack` | Purchase order acknowledgement | 855 | ORDRSP (231) | X12, EDIFACT |
| `order_change` | Purchase order change request | 860 | ORDCHG | X12, EDIFACT |
| `order_change_ack` | Purchase order change acknowledgement | 865 | ORDRSP (231) | X12, EDIFACT |
| `grocery_order` | Grocery products purchase order | 875 |  | catalogue only |
| `grocery_order_change` | Grocery products purchase order change | 876 |  | catalogue only |
| `rfq` | Request for quotation | 840 | REQOTE | EDIFACT |
| `quote` | Quotation | 843 | QUOTES | EDIFACT |
| `order_status_inquiry` | Order status inquiry | 869 | OSTENQ | EDIFACT |
| `order_status_report` | Order status report | 870 | OSTRPT | EDIFACT |

**fulfilment**

| Key | Name | X12 | EDIFACT (BGM) | Writers |
|---|---|---|---|---|
| `asn` | Advance ship notice (despatch advice) | 856 | DESADV (351) | X12, EDIFACT, IDoc |
| `receiving_advice` | Receiving advice | 861 | RECADV (632) | EDIFACT |
| `shipment_discrepancy` | Shipment delivery discrepancy | 854 |  | catalogue only |
| `warehouse_shipping_order` | Warehouse shipping order | 940 | INSDES | catalogue only |
| `warehouse_shipping_advice` | Warehouse shipping advice | 945 |  | catalogue only |
| `load_tender` | Load tender (transport order) | 204 | IFTMIN | catalogue only |
| `load_tender_response` | Response to a load tender | 990 |  | catalogue only |
| `shipment_status` | Shipment status message | 214 | IFTSTA | catalogue only |
| `booking_request` | Transport booking request | 300 | IFTMBF | catalogue only |
| `booking_confirmation` | Transport booking confirmation | 301 | IFTMBC | catalogue only |

**invoicing**

| Key | Name | X12 | EDIFACT (BGM) | Writers |
|---|---|---|---|---|
| `invoice` | Invoice | 810 | INVOIC (380) | X12, EDIFACT, IDoc |
| `credit_note` | Credit note (credit/debit adjustment) | 812 | INVOIC (381) | EDIFACT |
| `debit_note` | Debit note |  | INVOIC (383) | EDIFACT |
| `self_billed_invoice` | Self-billed invoice |  | INVOIC (389) | EDIFACT |
| `consolidated_invoice` | Consolidated invoice / statement | 811 | INVOIC (385) | EDIFACT |
| `grocery_invoice` | Grocery products invoice | 880 |  | catalogue only |
| `payment_remittance` | Payment order / remittance advice | 820 | REMADV (481) | X12, EDIFACT |
| `payment_order` | Payment order |  | PAYORD | catalogue only |
| `freight_invoice` | Freight details and invoice | 210 |  | catalogue only |

**inventory**

| Key | Name | X12 | EDIFACT (BGM) | Writers |
|---|---|---|---|---|
| `inventory` | Inventory report | 846 | INVRPT (35) | X12, EDIFACT, IDoc |
| `product_activity` | Product activity data | 852 | SLSRPT | EDIFACT |
| `resale_report` | Product transfer and resale report | 867 |  | catalogue only |
| `stock_transfer_shipment` | Warehouse stock transfer shipment advice | 943 |  | catalogue only |
| `stock_transfer_receipt` | Warehouse stock transfer receipt advice | 944 |  | catalogue only |
| `inventory_adjustment` | Warehouse inventory adjustment advice | 947 |  | catalogue only |

**returns claims**

| Key | Name | X12 | EDIFACT (BGM) | Writers |
|---|---|---|---|---|
| `return` | Return merchandise authorization | 180 | RETANN (7) | X12, EDIFACT, IDoc |
| `chargeback` | Chargeback (product transfer account adjustment) | 844 | COMDIS (385) | X12, EDIFACT, IDoc |
| `chargeback_response` | Chargeback response | 849 |  | catalogue only |
| `material_claim` | Material claim | 847 |  | catalogue only |
| `loss_damage_claim` | Loss or damage claim | 920 |  | catalogue only |
| `deduction_report` | Deduction research report | 891 |  | catalogue only |
| `dsd_delivery_return` | Delivery/return base record | 894 |  | catalogue only |
| `dsd_delivery_return_ack` | Delivery/return acknowledgment or adjustment | 895 |  | catalogue only |

**compliance**

| Key | Name | X12 | EDIFACT (BGM) | Writers |
|---|---|---|---|---|
| `functional_ack` | Functional acknowledgement | 997 |  | X12 |
| `implementation_ack` | Implementation acknowledgement | 999 |  | catalogue only |
| `application_advice` | Application advice | 824 | APERAK (312) | X12, EDIFACT |
| `nonconformance` | Nonconformance report | 842 |  | catalogue only |
| `test_results` | Report of test results | 863 |  | catalogue only |
| `material_safety_data_sheet` | Material safety data sheet | 848 |  | catalogue only |
| `customs_declaration` | Customs declaration |  | CUSDEC | catalogue only |
| `customs_response` | Customs response |  | CUSRES | catalogue only |

**master data**

| Key | Name | X12 | EDIFACT (BGM) | Writers |
|---|---|---|---|---|
| `price_catalog` | Price/sales catalog | 832 | PRICAT | EDIFACT |
| `item_maintenance` | Item maintenance (product data) | 888 | PRODAT | catalogue only |
| `party_data` | Trading partner profile (party information) | 838 | PARTIN | catalogue only |
| `product_dimensions` | Product dimension maintenance | 896 |  | catalogue only |
| `price_information` | Price information | 879 |  | catalogue only |
| `promotion` | Promotion announcement | 889 |  | catalogue only |

`order_ack` and `order_change_ack` share EDIFACT ORDRSP and its document name code; see the
note under Parsing.

## Canonical document

`Document` keeps the original fields (type, number, date, buyer, seller, shipTo, currency,
references, lines, ssccs) and adds optional generic ones that registry types use:

| Field | Meaning |
|---|---|
| `status` | Canonical status (accepted, changed, rejected ...), allowed values per type |
| `dates` | Named dates, `YYYY-MM-DD`: `delivery_requested`, `ship`, `delivery_estimated`, `delivered`, `received`, `effective`, `expiry`, `period_start`, `period_end` |
| `parties` | Further parties: `shipFrom`, `billTo`, `carrier`, `manufacturer`, `payer`, `payee` |
| `amounts` | Header amounts: `total` (computed from the lines when a type declares it), `payment`, `tax` |
| `note` | Free text |
| `lines[].status`, `.dates` | Line status and line dates (forecast buckets) |
| `lines[].ref`, `.refType`, `.amount` | Reference lines (remittances, acknowledgements): the referenced document, its kind and an amount |

References use the keys `po`, `asn`, `invoice`, `contract`, `bol`, `deliveryNote`, `shipment`,
`other`; other keys are stored but not written to X12 or EDIFACT.

## Validation

`Document.Validate` applies defaults (date, currency `USD`, line numbers, unit `EA` for item
lines) and then checks the type's `Required` fields (`number`, `lines`, `lines.gtin`,
`dates.period_start`, `references.po`, `parties.carrier`, `amounts.payment` ...), that statuses,
dates, parties and amounts are among those the type declares, and its rules: `qty_positive`,
`qty_nonneg`, `price_nonneg`, `currency_code`, `gtin_digits`, `date_not_after`, `one_of_refs`,
`amount_sum`, `reftype_known`, `x12_set_refs`. The first problem is returned. The six original types
validate exactly as before.

## Rendering and parsing

| Function | Purpose |
|---|---|
| `X12(doc, env)` | X12 004010 interchange with one transaction set; `""` when the type has no X12 writer |
| `ParseX12(s)` | Reads the 850 and every type with an X12 template (830, 862, 855, 860, 865, 820, 824, 997, 861, 852, 867, 990, 999) |
| `EDIFACT(doc, env)` | UN/EDIFACT D.96A interchange with one message; `""` when the type has no writer |
| `ParseEDIFACT(s)` | Reads a message into the registry type chosen by message type and document name code; where several types share both it returns the first that validates |
| `ParseEDIFACTAs(s, key)` | Same, with the type named by the caller |
| `ParseSegments(s)` | Syntax-level reader for any X12 or EDIFACT interchange: separators from ISA or UNA (release character honoured), envelope and message segments, each element split into components. Used by the mapping studio to offer segments and elements as source fields; it interprets nothing and accepts any transaction set or message |
| `IDoc(doc, env)` | IDoc XML for the six original types (write only); error for other types |
| `X12Set`, `EDIFACTType`, `IDocType`, `Lookup`, `LookupX12`, `LookupEDIFACT`, `Keys`, `Infos` | Registry lookups |

The six original types keep their hand-written X12 writers (856 with its HL hierarchy, 810, 846,
180, 844; the 850 reader). Template-driven X12 types are described by a header (segment IDs with
element roles such as `number`, `date:period_start`, `status`, `ref:po`, `amount:payment`), a line
profile (`po1ack`, `poc`, `fst`, `rmt`, `oti`, `ak2`, `lin`, `none`) and status code tables; the same template
drives writing and reading. The `lin` profile (861, 852, 867) writes LIN with the GTIN, the set's own quantity segment (RCD, ZA, SN1),
PID, the lot as REF*LT, the expiry as DTM*036, a reason as NTE*ADD and the line status as MSG text,
because no code list for it is verified; `none` is for sets that are their header (990); 999 reuses
the `ak2` profile with IK5. The generic EDIFACT writer (which also covers IFTMIN, IFTSTA, IFTMBF,
IFTMBC, INSDES, PAYORD, CUSDEC, CUSRES, PRODAT and PARTIN) emits one common segment set (BGM, DTM,
RFF, NAD, CUX, LIN with IMD/QTY/PIA/PRI/GIN/FTX, UNS, MOA, CNT) parameterised by the registry
entry. It does not generate message-specific mandatory segments beyond that, so conformance with
an implementation guide must be checked per message before production use.

`Envelope` carries sender, receiver and control numbers. The interchange sender is the buyer for
types whose flow is buyer to seller.

## Spec gaps

Every registry entry with an unverified identifier carries a `TODO(spec)` note in its `TODO`
field (56 entries); the consolidated list is in `docs/OPEN_QUESTIONS.md` (B2B transaction
types, X12 segment templates, EDIFACT common subset, ORDRSP type ambiguity). EDIFACT CONTRL
(syntax and service report) has no writer: its UCI/UCM structure refers to EDIFACT messages while the
canonical `functional_ack` refers to X12 sets, so it needs its own canonical type.

## Tests

From `backend/`: `go test ./pkg/edi` (no database). Table-driven tests prove every registry
entry is internally consistent (unique keys and X12 codes, well-formed codes, required fields and
rules known, template roles valid, status tables complete); every X12 template and every EDIFACT
writer round-trips a generated document (document to message, parse, equal); hand-written samples
cover forecast, order acknowledgement, remittance, functional and application acknowledgement,
credit note and receiving advice; the 861, 852, 867, 990 and 999 sets and the transport, payment
order, party and customs messages have their own samples; `ParseSegments` is tested on custom
separators, release characters and malformed input; `TestFormatCounts` pins the writer and reader
counts above. Not covered: conformance to any implementation guide (see Spec gaps).
