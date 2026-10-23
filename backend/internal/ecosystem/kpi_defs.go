package ecosystem

// The KPI library. Every entry is a formula over tables that already exist; none needs data the
// platform does not record. Where a KPI needs data that only exists when a tenant uses a feature
// (receiving advices for OTIF, partner SLAs for breaches) its description says so, and the value
// is null while there is nothing to measure.
//
// Event queries select (tenant_id, day, a, b) inside the window $1 (inclusive) .. $2 (exclusive).
// Tenant isolation comes from row-level security in tenant transactions; the peer comparison runs
// the same query in an operator transaction and groups by tenant_id.

// utcDay renders the UTC calendar day of a timestamptz expression.
func utcDay(expr string) string { return "(" + expr + " at time zone 'UTC')::date" }

// otifCTE derives, per inbound purchase order that states a requested delivery date and has at
// least one receiving advice from the buyer, whether it was delivered on time (the latest
// receiving advice is dated on or before the requested date) and in full (for every ordered GTIN
// the accepted received quantity reaches the ordered quantity). Orders without a requested date
// or without a receiving advice are not measured.
const otifCTE = `with ord as (
    select d.tenant_id, d.id, d.number, (d.payload #>> '{dates,delivery_requested}')::date as req
    from b2b_document d
    where d.type = 'order' and d.direction = 'inbound' and d.payload #>> '{dates,delivery_requested}' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'),
  adv as (
    select o.id as order_id, r.id as adv_id, (r.payload #>> '{dates,received}')::date as received_on
    from ord o join b2b_document r on r.tenant_id = o.tenant_id and r.type = 'receiving_advice' and r.direction = 'inbound'
      and r.payload #>> '{references,po}' = o.number
    where r.payload #>> '{dates,received}' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'),
  done as (select order_id, max(received_on) as received_on from adv group by order_id),
  ordq as (
    select o.id as order_id, l ->> 'gtin' as gtin, sum((l ->> 'quantity')::numeric) as qty
    from ord o join b2b_document d on d.id = o.id cross join lateral jsonb_array_elements(` + "case when jsonb_typeof(d.payload -> 'lines') = 'array' then d.payload -> 'lines' else '[]'::jsonb end" + `) l
    group by o.id, l ->> 'gtin'),
  recq as (
    select a.order_id, l ->> 'gtin' as gtin, sum((l ->> 'quantity')::numeric) as qty
    from adv a join b2b_document r on r.id = a.adv_id cross join lateral jsonb_array_elements(` + "case when jsonb_typeof(r.payload -> 'lines') = 'array' then r.payload -> 'lines' else '[]'::jsonb end" + `) l
    where coalesce(l ->> 'status', 'accepted') = 'accepted'
    group by a.order_id, l ->> 'gtin'),
  infull as (
    select q.order_id, bool_and(coalesce(r.qty, 0) >= q.qty) as ok
    from ordq q left join recq r on r.order_id = q.order_id and r.gtin = q.gtin
    group by q.order_id),
  delivered as (
    select o.tenant_id, dn.received_on as day, (dn.received_on <= o.req) as on_time, f.ok as in_full
    from ord o join done dn on dn.order_id = o.id join infull f on f.order_id = o.id
    where dn.received_on >= ($1::timestamptz at time zone 'UTC')::date and dn.received_on <= ($2::timestamptz at time zone 'UTC')::date)
  `

const saleableStatuses = `('active','in_progress','sellable_accessible','sellable_not_accessible','observed','commissioned')`

// Library is the KPI library.
var Library = []KPI{
	// --- delivery ---
	{ID: "otif_pct", Name: "On time, in full", Category: "delivery", Unit: "%", Direction: HigherBetter, Kind: KindRatio, Scale: 100, Peer: true,
		Description: "Share of customer purchase orders delivered on time and in full. On time: the latest receiving advice from the buyer is dated on or before the order's requested delivery date. In full: for every ordered GTIN the accepted received quantity reaches the ordered quantity. Measured only for orders with a requested delivery date and a receiving advice.",
		SQL:         otifCTE + `select tenant_id, day, (on_time and in_full)::int::float8, 1::float8 from delivered`},
	{ID: "on_time_pct", Name: "Delivered on time", Category: "delivery", Unit: "%", Direction: HigherBetter, Kind: KindRatio, Scale: 100, Peer: true,
		Description: "Share of measured customer purchase orders whose latest receiving advice is dated on or before the requested delivery date.",
		SQL:         otifCTE + `select tenant_id, day, on_time::int::float8, 1::float8 from delivered`},
	{ID: "in_full_pct", Name: "Delivered in full", Category: "delivery", Unit: "%", Direction: HigherBetter, Kind: KindRatio, Scale: 100, Peer: true,
		Description: "Share of measured customer purchase orders whose accepted received quantity reaches the ordered quantity for every GTIN.",
		SQL:         otifCTE + `select tenant_id, day, in_full::int::float8, 1::float8 from delivered`},
	{ID: "order_to_asn_hours", Name: "Order to ship notice", Category: "delivery", Unit: "h", Direction: LowerBetter, Kind: KindRatio, Peer: true,
		Description: "Mean hours from receiving a customer purchase order to the first despatch advice that references it (by PO number).",
		SQL: `select o.tenant_id, ` + utcDay("a.first_asn") + `, extract(epoch from a.first_asn - o.created_at)::float8 / 3600, 1::float8
			from b2b_document o
			cross join lateral (select min(x.created_at) as first_asn from b2b_document x
				where x.tenant_id = o.tenant_id and x.type = 'asn' and x.direction = 'outbound' and x.payload #>> '{references,po}' = o.number) a
			where o.type = 'order' and o.direction = 'inbound' and a.first_asn >= $1 and a.first_asn < $2`},

	// --- partner exchange ---
	{ID: "b2b_ack_rate_pct", Name: "Documents acknowledged", Category: "exchange", Unit: "%", Direction: HigherBetter, Kind: KindRatio, Scale: 100, Peer: true,
		Description: "Share of outbound B2B documents sent in the period that have been acknowledged (local delivery, a peer accepting the federation message, or a functional acknowledgement).",
		SQL: `select tenant_id, ` + utcDay("sent_at") + `, (acked_at is not null)::int::float8, 1::float8 from b2b_document
			where direction = 'outbound' and status = 'sent' and sent_at >= $1 and sent_at < $2`},
	{ID: "message_failure_pct", Name: "Partner message failures", Category: "exchange", Unit: "%", Direction: LowerBetter, Kind: KindRatio, Scale: 100, Peer: true,
		Description: "Share of outbound partner messages that failed for good: federation messages whose delivery job exhausted its retries, and shipments whose transmission failed, over all outbound messages and transmitted shipments.",
		SQL: `select tenant_id, day, a, b from (
				select fm.tenant_id, ` + utcDay("fm.created_at") + ` as day, coalesce(j.status = 'failed', false)::int::float8 as a, 1::float8 as b
				from federation_message fm left join job j on j.id = fm.job_id
				where fm.direction = 'outbound' and fm.created_at >= $1 and fm.created_at < $2
				union all
				select s.tenant_id, ` + utcDay("s.created_at") + `, (s.status = 'send_failed')::int::float8, 1::float8
				from shipment s where s.direction = 'outbound' and s.status in ('sent', 'send_failed') and s.created_at >= $1 and s.created_at < $2) x`},
	{ID: "sla_breaches", Name: "Partner SLA breaches", Category: "exchange", Unit: "count", Direction: LowerBetter, Kind: KindSum,
		Description: "Alerts raised because an outbound message was not acknowledged within the partner's SLA window. Needs partner SLAs to be set.",
		SQL: `select tenant_id, ` + utcDay("created_at") + `, 1::float8, 1::float8 from alert
			where kind = 'link.sla_breach' and created_at >= $1 and created_at < $2`},
	{ID: "b2b_documents", Name: "B2B documents exchanged", Category: "exchange", Unit: "count", Direction: HigherBetter, Kind: KindSum,
		Description: "B2B documents sent or received in the period (drafts excluded).",
		SQL: `select tenant_id, ` + utcDay("coalesce(sent_at, created_at)") + `, 1::float8, 1::float8 from b2b_document
			where status <> 'draft' and coalesce(sent_at, created_at) >= $1 and coalesce(sent_at, created_at) < $2`},

	// --- compliance ---
	{ID: "report_first_pass_pct", Name: "Regulatory reports accepted first time", Category: "compliance", Unit: "%", Direction: HigherBetter, Kind: KindRatio, Scale: 100, Peer: true,
		Description: "Share of regulatory reports that reached a final state (accepted, rejected or failed) and were accepted on the first submission.",
		SQL: `select tenant_id, ` + utcDay("created_at") + `, (status = 'accepted' and attempts <= 1)::int::float8, 1::float8 from regulatory_report
			where status in ('accepted', 'rejected', 'failed') and created_at >= $1 and created_at < $2`},
	{ID: "report_turnaround_hours", Name: "Report turnaround", Category: "compliance", Unit: "h", Direction: LowerBetter, Kind: KindRatio, Peer: true,
		Description: "Mean hours from creating a regulatory report to its acceptance by the authority or hub, for reports accepted in the period.",
		SQL: `select tenant_id, ` + utcDay("updated_at") + `, extract(epoch from updated_at - created_at)::float8 / 3600, 1::float8 from regulatory_report
			where status = 'accepted' and updated_at >= $1 and updated_at < $2`},
	{ID: "capture_rejection_pct", Name: "EPCIS documents rejected", Category: "compliance", Unit: "%", Direction: LowerBetter, Kind: KindRatio, Scale: 100, Peer: true,
		Description: "Share of EPCIS capture requests rejected by validation.",
		SQL: `select tenant_id, ` + utcDay("created_at") + `, (status = 'rejected')::int::float8, 1::float8 from epcis_capture
			where created_at >= $1 and created_at < $2`},
	{ID: "events_captured", Name: "EPCIS events captured", Category: "compliance", Unit: "count", Direction: HigherBetter, Kind: KindSum,
		Description: "Events in EPCIS captures that were not rejected.",
		SQL: `select tenant_id, ` + utcDay("created_at") + `, event_count::float8, 1::float8 from epcis_capture
			where status <> 'rejected' and created_at >= $1 and created_at < $2`},
	{ID: "certificates_expiring", Name: "Supplier certificates expiring in 30 days", Category: "compliance", Unit: "count", Direction: LowerBetter, Kind: KindSnapshot,
		Description: "Supplier certificates whose validity ends within the next 30 days. A snapshot as of now; no history is kept.",
		SQL:         `select count(*)::float8 from supplier_record where category = 'certificate' and valid_to between current_date and current_date + 30`},
	{ID: "certificates_expired", Name: "Supplier certificates expired", Category: "compliance", Unit: "count", Direction: LowerBetter, Kind: KindSnapshot,
		Description: "Supplier certificates whose validity ended before today. A snapshot as of now; no history is kept.",
		SQL:         `select count(*)::float8 from supplier_record where category = 'certificate' and valid_to < current_date`},

	// --- operations ---
	{ID: "alert_mttr_hours", Name: "Mean time to resolve alerts", Category: "operations", Unit: "h", Direction: LowerBetter, Kind: KindRatio, Peer: true,
		Description: "Mean hours from raising an alert to resolving it, for alerts resolved in the period.",
		SQL: `select tenant_id, ` + utcDay("resolved_at") + `, extract(epoch from resolved_at - created_at)::float8 / 3600, 1::float8 from alert
			where status = 'resolved' and resolved_at >= $1 and resolved_at < $2`},
	{ID: "alerts_open", Name: "Open alerts", Category: "operations", Unit: "count", Direction: LowerBetter, Kind: KindSnapshot,
		Description: "Alerts raised and not yet resolved. History is rebuilt from raise and resolve times.",
		SQL:         `select count(*)::float8 from alert where status = 'open'`,
		SeriesSQL: `select d::date, count(a.id)::float8
			from generate_series(($1::timestamptz at time zone 'UTC')::date, ($2::timestamptz at time zone 'UTC')::date, interval '1 day') d
			left join alert a on a.created_at < (d + interval '1 day') at time zone 'UTC' and (a.resolved_at is null or a.resolved_at >= (d + interval '1 day') at time zone 'UTC')
			group by d order by d`},
	{ID: "alerts_raised", Name: "Alerts raised", Category: "operations", Unit: "count", Direction: LowerBetter, Kind: KindSum,
		Description: "Alerts raised in the period, of any kind.",
		SQL:         `select tenant_id, ` + utcDay("created_at") + `, 1::float8, 1::float8 from alert where created_at >= $1 and created_at < $2`},
	{ID: "cases_open", Name: "Open cases", Category: "operations", Unit: "count", Direction: LowerBetter, Kind: KindSnapshot,
		Description: "Cases not yet resolved, broken down by age. A snapshot as of now.",
		SQL:         `select count(*)::float8 from work_case where status <> 'resolved'`,
		BucketSQL: `select b.label, count(c.id) as n
			from (values (1, '0-2 days'), (2, '3-7 days'), (3, '8-30 days'), (4, 'over 30 days')) b(ord, label)
			left join work_case c on c.status <> 'resolved' and (case when now() - c.created_at < interval '3 days' then 1 when now() - c.created_at < interval '8 days' then 2
				when now() - c.created_at < interval '31 days' then 3 else 4 end) = b.ord
			group by b.ord, b.label order by b.ord`},
	{ID: "cases_overdue", Name: "Cases past due", Category: "operations", Unit: "count", Direction: LowerBetter, Kind: KindSnapshot,
		Description: "Unresolved cases whose due time has passed. A snapshot as of now.",
		SQL:         `select count(*)::float8 from work_case where status <> 'resolved' and due_at < now()`},
	{ID: "case_resolution_hours", Name: "Median case resolution time", Category: "operations", Unit: "h", Direction: LowerBetter, Kind: KindQuantile, Q: 0.5, Peer: true,
		Description: "Median hours from creating a case to resolving it, for cases resolved in the period.",
		SQL: `select tenant_id, ` + utcDay("updated_at") + `, extract(epoch from updated_at - created_at)::float8 / 3600, 0::float8 from work_case
			where status = 'resolved' and updated_at >= $1 and updated_at < $2`},
	{ID: "investigation_resolution_hours", Name: "Investigation duration", Category: "operations", Unit: "h", Direction: LowerBetter, Kind: KindRatio, Peer: true,
		Description: "Mean hours from opening an investigation (tracing request, suspect or illegitimate product) to closing it, for investigations closed in the period.",
		SQL: `select tenant_id, ` + utcDay("closed_at") + `, extract(epoch from closed_at - created_at)::float8 / 3600, 1::float8 from investigation
			where closed_at >= $1 and closed_at < $2`},
	{ID: "investigation_on_time_pct", Name: "Tracing requests answered on time", Category: "operations", Unit: "%", Direction: HigherBetter, Kind: KindRatio, Scale: 100, Peer: true,
		Description: "Share of tracing requests with a due time that were answered on or before it, for requests answered in the period.",
		SQL: `select tenant_id, ` + utcDay("answered_at") + `, (answered_at <= due_at)::int::float8, 1::float8 from investigation
			where kind = 'tracing_request' and due_at is not null and answered_at >= $1 and answered_at < $2`},
	{ID: "investigations_overdue", Name: "Investigations past due", Category: "operations", Unit: "count", Direction: LowerBetter, Kind: KindSnapshot,
		Description: "Investigations not closed whose due time has passed. A snapshot as of now.",
		SQL:         `select count(*)::float8 from investigation where status <> 'closed' and due_at is not null and due_at < now()`},

	// --- verification ---
	{ID: "vrs_inbound_p95_ms", Name: "Verification response time (p95)", Category: "verification", Unit: "ms", Direction: LowerBetter, Kind: KindQuantile, Q: 0.95, Peer: true,
		Description: "95th percentile of the time this node took to answer inbound product verification requests.",
		SQL: `select tenant_id, ` + utcDay("created_at") + `, latency_ms::float8, 0::float8 from vrs_request
			where direction = 'inbound' and created_at >= $1 and created_at < $2`},
	{ID: "vrs_outbound_p95_ms", Name: "Partner verification latency (p95)", Category: "verification", Unit: "ms", Direction: LowerBetter, Kind: KindQuantile, Q: 0.95, Peer: true,
		Description: "95th percentile of the time responders took to answer this node's outbound verification requests.",
		SQL: `select tenant_id, ` + utcDay("created_at") + `, latency_ms::float8, 0::float8 from vrs_request
			where direction = 'outbound' and created_at >= $1 and created_at < $2`},
	{ID: "vrs_failure_pct", Name: "Verification request failures", Category: "verification", Unit: "%", Direction: LowerBetter, Kind: KindRatio, Scale: 100, Peer: true,
		Description: "Share of outbound verification requests that failed in transport (no usable answer from the responder).",
		SQL: `select tenant_id, ` + utcDay("created_at") + `, (reason like 'responder_error:%')::int::float8, 1::float8 from vrs_request
			where direction = 'outbound' and created_at >= $1 and created_at < $2`},
	{ID: "vrs_not_verified_pct", Name: "Verification requests not verified", Category: "verification", Unit: "%", Direction: LowerBetter, Kind: KindRatio, Scale: 100, Peer: true,
		Description: "Share of answered outbound verification requests where the responder did not verify the product.",
		SQL: `select tenant_id, ` + utcDay("created_at") + `, (not coalesce(verified, false))::int::float8, 1::float8 from vrs_request
			where direction = 'outbound' and reason not like 'responder_error:%' and created_at >= $1 and created_at < $2`},

	// --- inventory ---
	{ID: "serial_pool_utilisation_pct", Name: "Serial pool utilisation", Category: "inventory", Unit: "%", Direction: LowerBetter, Kind: KindSnapshot,
		Description: "Serial numbers issued over total capacity across active serial pools. A snapshot as of now.",
		SQL:         `select (100.0 * sum(next_counter) / nullif(sum(capacity), 0))::float8 from serial_pool where status = 'active'`},
	{ID: "serial_pools_low", Name: "Serial pools running low", Category: "inventory", Unit: "count", Direction: LowerBetter, Kind: KindSnapshot,
		Description: "Active serial pools whose remaining capacity is under their low-watermark percentage. A snapshot as of now.",
		SQL:         `select count(*)::float8 from serial_pool where status = 'active' and (capacity - next_counter)::numeric * 100 < capacity::numeric * low_watermark_pct`},
	{ID: "expiry_risk_packs", Name: "Packs expiring within 90 days", Category: "inventory", Unit: "count", Direction: LowerBetter, Kind: KindSnapshot,
		Description: "Serialised packs in a saleable state whose expiry date falls within the next 90 days, broken down by time left. A snapshot as of now.",
		SQL:         `select count(*)::float8 from serial_state where expiry between current_date and current_date + 90 and status in ` + saleableStatuses,
		BucketSQL: `select b.label, count(s.epc) as n
			from (values (1, 'within 30 days'), (2, '31-60 days'), (3, '61-90 days')) b(ord, label)
			left join serial_state s on s.status in ` + saleableStatuses + ` and s.expiry between current_date and current_date + 90
				and (case when s.expiry <= current_date + 30 then 1 when s.expiry <= current_date + 60 then 2 else 3 end) = b.ord
			group by b.ord, b.label order by b.ord`},
	{ID: "expired_in_stock", Name: "Expired packs still saleable", Category: "inventory", Unit: "count", Direction: LowerBetter, Kind: KindSnapshot,
		Description: "Serialised packs past their expiry date that are still in a saleable state. A snapshot as of now.",
		SQL:         `select count(*)::float8 from serial_state where expiry < current_date and status in ` + saleableStatuses},

	// --- recall and cold chain ---
	{ID: "recall_ack_hours", Name: "Recall response time", Category: "safety", Unit: "h", Direction: LowerBetter, Kind: KindRatio, Peer: true,
		Description: "Mean hours from notifying a customer of a recall to their acknowledgement, for acknowledgements received in the period.",
		SQL: `select tenant_id, ` + utcDay("acknowledged_at") + `, extract(epoch from acknowledged_at - sent_at)::float8 / 3600, 1::float8 from recall_notification
			where acknowledged_at >= $1 and acknowledged_at < $2`},
	{ID: "recall_ack_pct", Name: "Recall notices acknowledged", Category: "safety", Unit: "%", Direction: HigherBetter, Kind: KindRatio, Scale: 100, Peer: true,
		Description: "Share of recall notices sent in the period that the customer has acknowledged.",
		SQL: `select tenant_id, ` + utcDay("sent_at") + `, (acknowledged_at is not null)::int::float8, 1::float8 from recall_notification
			where sent_at >= $1 and sent_at < $2`},
	{ID: "coldchain_excursions", Name: "Cold-chain excursions", Category: "safety", Unit: "count", Direction: LowerBetter, Kind: KindSum,
		Description: "Temperature excursions detected in the period (by start time).",
		SQL: `select tenant_id, ` + utcDay("started_at") + `, 1::float8, 1::float8 from sensor_excursion
			where started_at >= $1 and started_at < $2`},
	{ID: "coldchain_excursion_minutes", Name: "Excursion duration", Category: "safety", Unit: "min", Direction: LowerBetter, Kind: KindRatio,
		Description: "Mean minutes outside the temperature range per excursion, for excursions that started in the period.",
		SQL: `select tenant_id, ` + utcDay("started_at") + `, minutes::float8, 1::float8 from sensor_excursion
			where started_at >= $1 and started_at < $2`},
	{ID: "coldchain_open_excursions", Name: "Open excursions", Category: "safety", Unit: "count", Direction: LowerBetter, Kind: KindSnapshot,
		Description: "Temperature excursions not yet closed. A snapshot as of now.",
		SQL:         `select count(*)::float8 from sensor_excursion where status = 'open'`},
}
