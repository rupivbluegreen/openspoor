# Open Spoor API contract (v1)

All endpoints are under `/api/v1`. JSON bodies, camelCase fields. Errors are RFC 9457
problem documents: `{ "title": "...", "status": 422, "errors": [...] }`.
Lists return `{ "items": [...], "total": N }`.

Auth: `Authorization: Bearer <jwt>` (from `POST /auth/login`) or `X-API-Key: osk_...`.
Roles: Admin, Serialization, Compliance, Ops, Integration, Viewer (Viewer = read-only GET).
The authoritative, machine-readable spec is `GET /api/v1/openapi.yaml`.

## Public (no auth)
| Method | Path | Notes |
|---|---|---|
| GET | /meta | `{name, version, mode, modules[], devAuth, oidc}` |
| POST | /auth/login | `{tenant, email, password}` → `{token, expiresAt, principal}` |
| POST | /saas/signup | SaaS mode only: `{companyName, slug, email, password, verticals[]}` → token |
| GET | /saas/branding?host= | tenant brand for login page `{name, slug, brand:{primaryColor, logoUrl}}` |
| POST | /verify/scan | pharmacy/consumer scan: `{code}` (DataMatrix text, HRI or Digital Link) → `{verified, status, product, pack, message}` |
| POST | /vrs/verify | GS1 Lightweight Messaging VRS responder (partner-facing; API key or mTLS in prod) |
| POST | /playground/validate | `{document}` (JSON-LD string or XML) → `{valid, format, eventCount, errors[]}` |
| POST | /playground/convert | `{document, to: "json"|"xml"}` → `{document}` |
| POST | /playground/visualize | `{document}` → `{nodes[], edges[], timeline[]}` |
| POST | /federation/inbox | signed node-to-node messages (M9) |

## Platform (M1)
| GET | /me | principal |
| GET | /me/tenants | tenants for tenant switcher |
| POST | /auth/switch | `{tenantId}` → new token |
| GET | /tenant | current tenant (name, plan, verticals, brand) |
| GET/POST | /orgs, PUT /orgs/{id} | `{name, kind, companyPrefix, country}` kinds: manufacturer, cmo, 3pl, wholesaler, pharmacy, hospital, regulator, grower, processor, distributor, retailer, restaurant, importer |
| GET/POST | /sites, PUT /sites/{id} | `{orgId, gln, name, kind, country, address}` |
| GET/POST | /licenses | `{orgId, siteId?, kind, number, authority, country, validFrom, validTo}` |
| GET/POST | /users, PATCH /users/{id} | `{email, name, roles[], password?, locale}`; patch `{name?, roles?, status?, reason}` |
| GET/POST | /api-keys, DELETE /api-keys/{id} | create returns `secret` once |
| GET | /audit?entity=&entityId=&action=&limit=&offset= | `{items:[{seq, at, actor, action, entity, entityId, before, after, reason, hash}], total}` |
| GET | /audit/verify | `{checked, intact, brokenAtSeq}` |
| POST | /esignatures | `{password, meaning, reason, entity, entityId}` (re-auth) |
| GET | /esignatures?entity=&entityId= | |
| GET | /alerts?status=open|resolved|all&kind= | shared alert inbox |
| POST | /alerts/{id}/resolve | |

## Master data (M1)
| GET/POST | /products, GET/PUT /products/{id} | `{gtin, name, description, companyPrefixLength, ndc, marketCodes:{}, targetMarkets[], dosageForm, strength, packSize, vertical}` |
| POST | /products/import | `text/csv` body (header: gtin,name,ndc,company_prefix_length,target_markets,dosage_form,strength,pack_size) → `{created, updated, errors[{line, message}]}` |
| GET/PUT | /products/{id}/packaging/{market} | `{levels:[{level:"unit"|"bundle"|"case"|"pallet"|"shipment", gtin?, quantity}]}` |

## Serialization (M2)
| GET/POST | /serial-pools | `{productId, market, mode:"random"|"sequential", alphabet?, length, prefix?, lowWatermarkPct?}` → pool with `capacity, issued, remaining` |
| GET | /serial-pools/{id} | |
| POST | /serial-pools/{id}/allocate | header `Idempotency-Key`; `{quantity}` → `{allocationId, poolId, gtin, serials[], start, count}` |
| POST | /codes/encode | `{gtin, serial, lot?, expiry?(YYMMDD), format?: "png"|"svg"|"text"}` → `{elementString, hri, digitalLink, urn, image?(data URI)}` |
| POST | /codes/decode | `{code}` → `{gtin, serial, lot, expiry, urn, digitalLink}` |

## EPCIS (M3)
| POST | /epcis/capture | EPCIS 2.0 JSON-LD or XML (1.2 accepted) body → 202 `{captureId, status, eventCount, errors[]}`; 422 with per-event errors when invalid |
| GET | /epcis/capture | recent capture jobs |
| GET | /epcis/capture/{id} | `{id, status, eventCount, errorCount, errors[], createdAt, finishedAt}` |
| GET | /epcis/events?eventType=&GE_eventTime=&LT_eventTime=&EQ_bizStep=&EQ_disposition=&MATCH_anyEPC=&EQ_bizLocation=&perPage=&nextPageToken= | EPCISQueryDocument (JSON) |
| GET/POST | /epcis/queries, GET /epcis/queries/{name}/events | named queries |
| GET/POST | /epcis/subscriptions, DELETE /epcis/subscriptions/{id} | `{queryName?, query?, dest, secret?}` webhooks (HMAC-SHA256 header `X-OpenSpoor-Signature`) |
| GET | /trace/serials/{epc} | `{state:{status, disposition, bizLocation, parentEpc, lastEventTime}, history:[events], children:[...], ancestors:[...]}` (epc URL-encoded URN or Digital Link) |
| GET | /trace/tree/{epc} | aggregation tree `{epc, children:[{epc, children}]}` |
| GET | /trace/stats | `{events, serials, captures24h, byStatus:{}}` |

## DSCSA (M4)
| GET/POST | /shipments | outbound/inbound shipments `{direction, partnerId, poNumber, shipDate, items[], status}` |
| POST | /shipments/{id}/tits | build TI/TS EPCIS document from shipping events |
| POST | /shipments/{id}/send | send via HTTPS/AS2 to partner endpoint |
| POST | /shipments/inbound | receive TI/TS (EPCIS doc + ASN) |
| POST | /shipments/{id}/reconcile | compare inbound TI/TS to ASN → `{matched, missing[], unexpected[]}` |
| GET/POST | /vrs/requests | VRS requester console `{gtin, serial, lot, expiry, requestorGln, context}` → `{verified, responder, latencyMs}` |
| GET/POST | /vrs/directory | lookup directory `{gtinPrefix, responderUrl, auth}` |

## Connectors + regulatory (M5)
| GET | /connectors | available connectors (eu-fmd-emvs, sa-rsd, …) with config schema |
| GET/POST | /connectors/instances | `{connector, name, config:{}, enabled}` |
| POST | /connectors/instances/{id}/submit | `{epcs[] | captureId}` → report |
| GET | /reports | regulatory reports `{id, connector, status, attempts, response, createdAt}` |
| POST | /reports/{id}/resubmit | |
| GET | /rulepacks | versioned YAML country rule packs |

## Partners (M6) and Link (M9)
| GET | /partners/directory?q= | public directory |
| GET/POST | /partners | connections `{name, gln, status, endpoints:{}, health}` |
| POST | /partners/invitations | `{email, name}` → invitation code; POST /partners/invitations/accept `{code}` |
| POST | /partners/{id}/approve | |
| GET/POST | /b2b/documents | orders, asn, invoice, inventory, return, chargeback; `{type, partnerId, payload}`; GET /b2b/documents/{id}/edi?format=x12|edifact|idoc |
| GET/POST | /suppliers/certificates | supplier compliance hub, expiry alerts |
| GET | /federation/peers, POST /federation/peers | node-to-node peers |

## SaaS (M7b) — operator routes require operator principal
| GET/PUT | /tenant/branding | `{primaryColor, logoUrl}` |
| GET | /tenant/usage | `{period, events, reports, activePartners, quota:{…}}` |
| GET | /tenant/features | feature flags for the tenant |
| POST | /tenant/export | GDPR export (zip/json) ; DELETE /tenant (with e-sig) |
| GET | /operator/tenants | all tenants with usage/health |
| PATCH | /operator/tenants/{id} | `{plan?, status?, quotas?}` |
| POST | /operator/tenants/{id}/impersonate | `{reason}` consented support access (audited) |

## Food (M8)
| GET/POST | /food/lots | traceability lots `{tlc, productId, quantity, uom, siteId, harvestDate, plotIds[]}` |
| GET/POST | /food/cte | critical tracking events `{type: harvesting|cooling|initial_packing|first_land_based_receiving|shipping|receiving|transformation, tlc, kdes:{}}` |
| POST | /food/transformations | `{inputs:[{tlc, quantity}], output:{tlc, productId, quantity}}` |
| GET/POST | /food/plots | farm/plot registry `{name, country, geojson, commodity}` |
| GET | /food/fsma204/export?from=&to=&tlc= | 24-hour sortable spreadsheet (CSV) |
| GET/POST | /food/recalls, GET /food/recalls/{id} | recall workflow, affected lots/customers |
| GET/POST | /food/eudr/dds | EUDR due diligence statements |

## Act (M10)
| GET | /agents | agent definitions |
| GET | /agents/actions?status=pending | proposed actions awaiting approval |
| POST | /agents/actions/{id}/approve, /reject, /revert | human gates, reversible |
| POST | /agents/{name}/run | run an agent now |
| GET | /control-tower | monitors + prioritized work queue (cases) |
| GET/POST | /cases, PATCH /cases/{id} | |
| GET/POST | /forms, /workflows | no-code designer definitions |
| POST | /assistant/chat | `{messages:[{role, content}]}` → `{reply, citations[]}` |

## Ecosystem (M11)
| GET/POST | /apps | marketplace registry |
| POST | /sensors/readings | cold-chain ingest `{deviceId, epc?, shipmentId?, readings:[{time, tempC}]}` |
| GET | /sensors/excursions | |
| GET/POST | /udi/devices | UDI (GUDID/EUDAMED) records + submission |
| GET | /benchmarks | latest measured performance numbers |
