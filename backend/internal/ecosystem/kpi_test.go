package ecosystem_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/ecosystem"
	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/pkg/edi"
)

type kpiItem struct {
	ID, Category, Unit, Direction, Kind, GoalStatus, Error string
	Value                                                  *float64
	Samples                                                int64
	SeriesAvailable                                        bool
	Breakdown                                              []struct {
		Label string
		Count int64
	}
	Goal *struct{ Target float64 }
}

type kpiList struct {
	Items      []kpiItem
	Total      int
	Days       int
	Categories []struct {
		Key   string
		Count int
	}
}

func (l kpiList) get(t *testing.T, id string) kpiItem {
	t.Helper()
	for _, k := range l.Items {
		if k.ID == id {
			return k
		}
	}
	t.Fatalf("KPI %s not listed", id)
	return kpiItem{}
}

func want(t *testing.T, k kpiItem, v float64) {
	t.Helper()
	if k.Error != "" {
		t.Fatalf("%s: %s", k.ID, k.Error)
	}
	if k.Value == nil || math.Abs(*k.Value-v) > 1e-3 {
		got := "null"
		if k.Value != nil {
			got = fmt.Sprint(*k.Value)
		}
		t.Fatalf("%s = %s, want %v", k.ID, got, v)
	}
}

type fixture struct {
	t    *testing.T
	c    *pgx.Conn
	tid  string
	base time.Time // today 00:00 UTC
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.c.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("%v\n%s", err, sql)
	}
}

// at returns today's midnight shifted by days and hours.
func (f *fixture) at(days int, hours float64) time.Time {
	return f.base.AddDate(0, 0, days).Add(time.Duration(hours * float64(time.Hour)))
}

func (f *fixture) ymd(days int) string { return f.base.AddDate(0, 0, days).Format("2006-01-02") }

func (f *fixture) doc(typ, direction, number, status string, created time.Time, payload map[string]any) string {
	f.t.Helper()
	if payload["number"] == nil {
		payload["number"] = number
	}
	payload["type"] = typ
	b, _ := json.Marshal(payload)
	var id string
	err := f.c.QueryRow(context.Background(), `insert into b2b_document (id, tenant_id, type, direction, number, status, payload, created_at, sent_at)
		values (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, case when $5::text = 'sent' then $7::timestamptz end) returning id::text`,
		f.tid, typ, direction, number, status, b, created).Scan(&id)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func lines(q ...any) []map[string]any {
	var out []map[string]any
	for i := 0; i+2 < len(q)+1 && i+1 < len(q); i += 3 {
		l := map[string]any{"gtin": q[i], "quantity": q[i+1]}
		if i+2 < len(q) && q[i+2] != "" {
			l["status"] = q[i+2]
		}
		out = append(out, l)
	}
	return out
}

func loadKPIs(t *testing.T, e *testkit.Env, query string) kpiList {
	t.Helper()
	var l kpiList
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/kpis"+query, nil), &l)
	return l
}

// TestKPIValuesFromFixtures computes every KPI of the library from known rows and checks the
// expected values, so a change to a formula shows up here.
func TestKPIValuesFromFixtures(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	now := time.Now().UTC()
	f := &fixture{t: t, c: conn, tid: e.Tenant.ID.String(), base: time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)}

	// --- delivery: six customer orders, four of them measurable ---
	const g1, g2 = "09506000134352", "09506000134369"
	order := func(n string, req int, created time.Time) {
		p := map[string]any{"lines": lines(g1, 10, "", g2, 5, "")}
		if req != 0 {
			p["dates"] = map[string]string{"delivery_requested": f.ymd(req)}
		}
		f.doc("order", "inbound", n, "received", created, p)
	}
	advice := func(n, po string, received int, ls []map[string]any) {
		f.doc("receiving_advice", "inbound", n, "received", f.at(received, 9), map[string]any{
			"references": map[string]string{"po": po}, "dates": map[string]string{"received": f.ymd(received)}, "lines": ls})
	}
	order("PO-1", -5, f.at(-10, 8)) // on time, in full
	advice("RA-1", "PO-1", -6, lines(g1, 10, "accepted", g2, 5, "accepted"))
	order("PO-2", -5, f.at(-9, 8)) // late, in full
	advice("RA-2", "PO-2", -3, lines(g1, 10, "accepted", g2, 5, "accepted"))
	order("PO-3", -2, f.at(-8, 8)) // on time, short: 4 of the 10 are damaged and do not count
	advice("RA-3", "PO-3", -4, lines(g1, 6, "accepted", g1, 4, "damaged", g2, 5, "accepted"))
	order("PO-4", -2, f.at(-7, 8)) // two advices: the latest is on time and together they are in full
	advice("RA-4a", "PO-4", -4, lines(g1, 6, "accepted", g2, 5, "accepted"))
	advice("RA-4b", "PO-4", -2, lines(g1, 4, "accepted"))
	order("PO-5", -2, f.at(-6, 8)) // no receiving advice: not measured
	order("PO-6", 0, f.at(-6, 8))  // no requested date: not measured
	f.doc("receiving_advice", "inbound", "RA-6", "received", f.at(-1, 9), map[string]any{"references": map[string]string{"po": "PO-6"}, "dates": map[string]string{"received": f.ymd(-1)}, "lines": lines(g1, 10, "accepted")})
	// Despatch advices: PO-1 shipped 6 h after the order, PO-2 12 h after.
	f.doc("asn", "outbound", "ASN-1", "draft", f.at(-10, 14), map[string]any{"references": map[string]string{"po": "PO-1"}, "lines": lines(g1, 10, "")})
	f.doc("asn", "outbound", "ASN-2", "draft", f.at(-9, 20), map[string]any{"references": map[string]string{"po": "PO-2"}, "lines": lines(g1, 10, "")})
	// A document without lines must not break the line arithmetic.
	f.doc("order", "inbound", "PO-7", "received", f.at(-3, 1), map[string]any{"lines": nil, "dates": map[string]string{"delivery_requested": f.ymd(-1)}})

	// --- exchange ---
	sent := f.at(-1, 1)
	for i, acked := range []bool{true, true, false} {
		id := f.doc("invoice", "outbound", fmt.Sprintf("INV-%d", i), "sent", sent, map[string]any{"lines": lines(g1, 1, "")})
		if acked {
			f.exec(`update b2b_document set acked_at = now() where id = $1`, id)
		}
	}
	for i, jobStatus := range []string{"done", "failed", "queued", ""} {
		var jobID *int64
		if jobStatus != "" {
			var j int64
			if err := conn.QueryRow(ctx, `insert into job (tenant_id, kind, payload, status) values ($1, 'federation.deliver', '{}', $2) returning id`, f.tid, jobStatus).Scan(&j); err != nil {
				t.Fatal(err)
			}
			jobID = &j
		}
		f.exec(`insert into federation_message (id, tenant_id, direction, msg_id, type, status, job_id, created_at) values (gen_random_uuid(), $1, 'outbound', $2, 'b2b.document', 'queued', $3, $4)`,
			f.tid, fmt.Sprintf("m%d", i), jobID, f.at(-2, 3))
	}
	f.exec(`insert into federation_message (id, tenant_id, direction, msg_id, type, status, created_at) values (gen_random_uuid(), $1, 'inbound', 'in1', 'b2b.document', 'delivered', $2)`, f.tid, f.at(-2, 3))
	f.exec(`insert into shipment (id, tenant_id, direction, status, created_at) values (gen_random_uuid(), $1, 'outbound', 'sent', $2), (gen_random_uuid(), $1, 'outbound', 'send_failed', $2), (gen_random_uuid(), $1, 'outbound', 'draft', $2)`, f.tid, f.at(-2, 3))
	for _, h := range []float64{1, 2} {
		f.exec(`insert into alert (id, tenant_id, kind, title, status, created_at) values (gen_random_uuid(), $1, 'link.sla_breach', 'late', 'open', $2)`, f.tid, f.at(-1, 12+h))
	}

	// --- compliance ---
	f.exec(`insert into connector_instance (id, tenant_id, connector, name) values ('00000000-0000-0000-0000-0000000000c1', $1, 'hub', 'hub')`, f.tid)
	report := func(key, status string, attempts int, created, updated time.Time) {
		f.exec(`insert into regulatory_report (id, tenant_id, instance_id, connector, kind, idem_key, payload, status, attempts, created_at, updated_at)
			values (gen_random_uuid(), $1, '00000000-0000-0000-0000-0000000000c1', 'hub', 'x', $2, '{}', $3, $4, $5, $6)`, f.tid, key, status, attempts, created, updated)
	}
	report("r1", "accepted", 1, f.at(-3, 0), f.at(-3, 2))
	report("r2", "accepted", 2, f.at(-3, 0), f.at(-3, 4))
	report("r3", "rejected", 1, f.at(-3, 0), f.at(-3, 1))
	report("r4", "failed", 1, f.at(-3, 0), f.at(-3, 1))
	report("r5", "queued", 0, f.at(-3, 0), f.at(-3, 0))
	for i, c := range []struct {
		status string
		events int
	}{{"accepted", 10}, {"accepted", 5}, {"completed", 7}, {"rejected", 0}, {"accepted", 3}} {
		f.exec(`insert into epcis_capture (id, tenant_id, status, format, event_count, created_by, created_at) values (gen_random_uuid(), $1, $2, 'json', $3, 'test', $4)`,
			f.tid, c.status, c.events, f.at(-2, float64(i)))
	}
	for _, c := range []struct {
		cat  string
		days int
	}{{"certificate", 10}, {"certificate", 60}, {"certificate", -5}, {"audit", 5}} {
		f.exec(`insert into supplier_record (id, tenant_id, category, kind, valid_to) values (gen_random_uuid(), $1, $2, 'GMP', current_date + $3::int)`, f.tid, c.cat, c.days)
	}

	// --- operations ---
	alert := func(title, status string, created time.Time, resolved *time.Time) {
		f.exec(`insert into alert (id, tenant_id, kind, title, status, created_at, resolved_at) values (gen_random_uuid(), $1, 'test', $2, $3, $4, $5)`, f.tid, title, status, created, resolved)
	}
	r1, r2 := f.at(-3, 6), f.at(-2, 12)
	alert("A1", "open", f.at(-3, 1), nil)
	alert("A2", "resolved", f.at(-5, 6), &r1)  // 48 h
	alert("A3", "resolved", f.at(-2, 10), &r2) // 2 h
	caseRow := func(status string, created, updated time.Time, due *time.Time) {
		f.exec(`insert into work_case (id, tenant_id, kind, title, status, created_at, updated_at, due_at) values (gen_random_uuid(), $1, 'k', 't', $2, $3, $4, $5)`, f.tid, status, created, updated, due)
	}
	past := f.at(-1, 0)
	caseRow("open", f.at(-1, 0), f.at(-1, 0), nil) // 0-2 days
	caseRow("open", f.at(-5, 0), f.at(-5, 0), nil) // 3-7
	caseRow("open", f.at(-10, 0), f.at(-10, 0), &past)
	caseRow("open", f.at(-40, 0), f.at(-40, 0), nil) // over 30
	caseRow("resolved", f.at(-4, 0), f.at(-4, 2), nil)
	caseRow("resolved", f.at(-4, 0), f.at(-4, 6), nil)
	inv := func(kind, status string, created time.Time, closed, answered, due *time.Time) {
		f.exec(`insert into investigation (id, tenant_id, kind, status, created_at, closed_at, answered_at, due_at) values (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7)`, f.tid, kind, status, created, closed, answered, due)
	}
	c1, a1, d1 := f.at(-3, 0), f.at(-4, 0), f.at(-3, 0) // created -5 closed -3: 48 h; answered before due
	c2, a2, d2 := f.at(-1, 0), f.at(-2, 0), f.at(-3, 0) // created -4 closed -1: 72 h; answered after due
	inv("tracing_request", "closed", f.at(-5, 0), &c1, &a1, &d1)
	inv("tracing_request", "closed", f.at(-4, 0), &c2, &a2, &d2)
	inv("suspect", "open", f.at(-2, 0), nil, nil, &past)

	// --- verification ---
	vrs := func(direction string, verified bool, reason string, ms int) {
		f.exec(`insert into vrs_request (id, tenant_id, direction, corr_uuid, gtin, serial, verified, reason, latency_ms, created_at) values (gen_random_uuid(), $1, $2, gen_random_uuid(), $3, 's', $4, $5, $6, $7)`,
			f.tid, direction, g1, verified, reason, ms, f.at(-1, 5))
	}
	for _, ms := range []int{10, 20, 30, 40, 100} {
		vrs("inbound", true, "", ms)
	}
	vrs("outbound", true, "", 5)
	vrs("outbound", true, "", 10)
	vrs("outbound", false, "lot_mismatch", 15)
	vrs("outbound", false, "responder_error: timeout", 20)

	// --- inventory ---
	var product string
	if err := conn.QueryRow(ctx, `insert into product (id, tenant_id, gtin, name) values (gen_random_uuid(), $1, $2, 'P') returning id::text`, f.tid, g1).Scan(&product); err != nil {
		t.Fatal(err)
	}
	for i, used := range []int{900, 100} {
		f.exec(`insert into serial_pool (id, tenant_id, product_id, gtin, market, mode, alphabet, length, prefix, key_material, next_counter, capacity, low_watermark_pct)
			values (gen_random_uuid(), $1, $2, $3, 'EU', 'random', 'abc', 8, $4, '\x00', $5, 1000, 20)`, f.tid, product, g1, fmt.Sprint(i), used)
	}
	for i, s := range []struct {
		status string
		days   int
	}{{"commissioned", 10}, {"sellable_accessible", 45}, {"active", 80}, {"commissioned", 120}, {"commissioned", -5}, {"decommissioned", -3}} {
		f.exec(`insert into serial_state (tenant_id, epc, gtin, status, expiry, last_event_time, last_event_id) values ($1, $2, $3, $4, current_date + $5::int, now(), 'e')`,
			f.tid, fmt.Sprintf("urn:epc:id:sgtin:%d", i), g1, s.status, s.days)
	}

	// --- recall and cold chain ---
	f.exec(`insert into recall (id, tenant_id, title, reason) values ('00000000-0000-0000-0000-0000000000d1', $1, 'R', 'x')`, f.tid)
	notif := func(sent time.Time, ack *time.Time) {
		f.exec(`insert into recall_notification (id, tenant_id, recall_id, customer_name, channel, sent_at, acknowledged_at) values (gen_random_uuid(), $1, '00000000-0000-0000-0000-0000000000d1', 'c', 'email', $2, $3)`, f.tid, sent, ack)
	}
	k1, k2 := f.at(-3, 18), f.at(-2, 6)
	notif(f.at(-3, 6), &k1) // 12 h
	notif(f.at(-3, 6), &k2) // 24 h
	notif(f.at(-3, 6), nil)
	for i, x := range []struct {
		minutes int
		status  string
	}{{10, "closed"}, {20, "closed"}, {30, "open"}} {
		f.exec(`insert into sensor_excursion (id, tenant_id, device_id, min_c, max_c, started_at, ended_at, peak_c, minutes, status) values (gen_random_uuid(), $1, 'dev', 2, 8, $2, $2, 11, $3, $4)`,
			f.tid, f.at(-2, float64(i)), x.minutes, x.status)
	}

	l := loadKPIs(t, e, "")
	if l.Days != 30 || l.Total != len(l.Items) || l.Total < 30 {
		t.Fatalf("listing: days %d total %d", l.Days, l.Total)
	}
	for _, k := range l.Items {
		if k.Error != "" {
			t.Errorf("%s: %s", k.ID, k.Error)
		}
	}
	for id, v := range map[string]float64{
		"otif_pct": 50, "on_time_pct": 75, "in_full_pct": 75, "order_to_asn_hours": 9,
		"b2b_ack_rate_pct": 200.0 / 3, "message_failure_pct": 100.0 * 2 / 6, "sla_breaches": 2,
		"report_first_pass_pct": 25, "report_turnaround_hours": 3, "capture_rejection_pct": 20, "events_captured": 25,
		"certificates_expiring": 1, "certificates_expired": 1,
		"alert_mttr_hours": 25, "alerts_open": 3, "alerts_raised": 5,
		"cases_open": 4, "cases_overdue": 1, "case_resolution_hours": 4,
		"investigation_resolution_hours": 60, "investigation_on_time_pct": 50, "investigations_overdue": 1,
		"vrs_inbound_p95_ms": 88, "vrs_outbound_p95_ms": 19.25, "vrs_failure_pct": 25, "vrs_not_verified_pct": 100.0 / 3,
		"serial_pool_utilisation_pct": 50, "serial_pools_low": 1, "expiry_risk_packs": 3, "expired_in_stock": 1,
		"recall_ack_hours": 18, "recall_ack_pct": 200.0 / 3, "coldchain_excursions": 3, "coldchain_excursion_minutes": 20, "coldchain_open_excursions": 1,
	} {
		want(t, l.get(t, id), v)
	}
	// B2B documents exchanged: 7 orders + 8 advices... counted from the fixture rows.
	var docs int
	if err := conn.QueryRow(ctx, `select count(*) from b2b_document where status <> 'draft'`).Scan(&docs); err != nil {
		t.Fatal(err)
	}
	want(t, l.get(t, "b2b_documents"), float64(docs))

	// Samples behind a ratio: the denominators.
	if s := l.get(t, "otif_pct").Samples; s != 4 {
		t.Errorf("OTIF measured 4 orders, got %d", s)
	}
	if b := l.get(t, "cases_open").Breakdown; len(b) != 4 || b[0].Count != 1 || b[1].Count != 1 || b[2].Count != 1 || b[3].Count != 1 || b[3].Label != "over 30 days" {
		t.Errorf("case age breakdown: %+v", b)
	}
	if b := l.get(t, "expiry_risk_packs").Breakdown; len(b) != 3 || b[0].Count != 1 || b[1].Count != 1 || b[2].Count != 1 {
		t.Errorf("expiry breakdown: %+v", b)
	}

	// Category filter and the window.
	if d := loadKPIs(t, e, "?category=delivery"); len(d.Items) != 4 {
		t.Errorf("delivery KPIs: %d", len(d.Items))
	}
	// A 1-day window sees only today's events: the fixtures are older.
	if one := loadKPIs(t, e, "?days=1"); one.get(t, "otif_pct").Value != nil || one.get(t, "alerts_raised").Value == nil || *one.get(t, "alerts_raised").Value != 0 {
		t.Errorf("one-day window: %+v %+v", one.get(t, "otif_pct"), one.get(t, "alerts_raised"))
	}
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/kpis?days=0", nil), nil)

	// --- series ---
	var s struct {
		ID        string
		Available bool
		Days      int
		Points    []struct {
			Day   string
			Value *float64
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/kpis/alerts_open/series?days=7", nil), &s)
	if !s.Available || len(s.Points) != 7 {
		t.Fatalf("alerts_open series: %+v", s)
	}
	byDay := map[string]float64{}
	for _, p := range s.Points {
		if p.Value != nil {
			byDay[p.Day] = *p.Value
		}
	}
	// At the end of day -4 only A2 is open; at the end of day -3 A1 is open and A2 has been resolved; today A1 and the two SLA alerts... and A3 is resolved.
	if byDay[f.ymd(-4)] != 1 || byDay[f.ymd(-3)] != 1 || byDay[f.ymd(-2)] != 1 || byDay[f.ymd(0)] != 3 {
		t.Errorf("alerts_open history: %v", byDay)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/kpis/alerts_raised/series?days=7", nil), &s)
	byDay = map[string]float64{}
	for _, p := range s.Points {
		if p.Value == nil {
			t.Errorf("a sum series has no gaps: %+v", p)
			continue
		}
		byDay[p.Day] = *p.Value
	}
	if byDay[f.ymd(-3)] != 1 || byDay[f.ymd(-1)] != 2 || byDay[f.ymd(-6)] != 0 || len(byDay) != 7 {
		t.Errorf("alerts_raised series: %v", byDay)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/kpis/otif_pct/series?days=30", nil), &s)
	nonNull := 0
	for _, p := range s.Points {
		if p.Value != nil {
			nonNull++
		}
	}
	if len(s.Points) != 30 || nonNull != 4 {
		t.Errorf("otif series: %d points, %d with data", len(s.Points), nonNull)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/kpis/cases_open/series", nil), &s)
	if s.Available || len(s.Points) != 0 {
		t.Errorf("a snapshot without history has no series: %+v", s)
	}
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/kpis/nope/series", nil), nil)
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/kpis/alerts_open/series?days=500", nil), nil)

	// --- goals: tags, roles and audit ---
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/kpis/otif_pct/goal", map[string]any{}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/kpis/otif_pct/goal", map[string]any{"target": 120}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/kpis/alerts_open/goal", map[string]any{"target": -1}), nil)
	e.Must(http.StatusNotFound, e.Do("PUT", "/api/v1/kpis/nope/goal", map[string]any{"target": 1}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/kpis/otif_pct/goal", "{"), nil)
	e.Must(http.StatusNotFound, e.Do("DELETE", "/api/v1/kpis/otif_pct/goal", nil), nil)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/kpis/otif_pct/goal", map[string]any{"target": 40, "note": "Q4"}), nil)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/kpis/alerts_open/goal", map[string]any{"target": 2}), nil)
	l = loadKPIs(t, e, "")
	if k := l.get(t, "otif_pct"); k.GoalStatus != "met" || k.Goal == nil || k.Goal.Target != 40 {
		t.Errorf("otif goal: %+v", k)
	}
	if k := l.get(t, "alerts_open"); k.GoalStatus != "missed" {
		t.Errorf("alerts_open goal (3 open, at most 2 wanted): %+v", k)
	}
	if l.get(t, "cases_open").Goal != nil {
		t.Error("goals are per KPI")
	}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/kpis/otif_pct/goal", map[string]any{"target": 60}), nil)
	if k := loadKPIs(t, e, "").get(t, "otif_pct"); k.GoalStatus != "missed" {
		t.Errorf("otif goal updated: %+v", k)
	}
	// A viewer reads but cannot write.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/users", map[string]any{"email": "viewer@acme.test", "roles": []string{"Viewer"}, "password": testkit.Password}), nil)
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "acme", "email": "viewer@acme.test", "password": testkit.Password}), &tok)
	e.Must(http.StatusOK, e.Call(tok.Token, "GET", "/api/v1/kpis", nil), nil)
	e.Must(http.StatusOK, e.Call(tok.Token, "GET", "/api/v1/kpis/otif_pct/series", nil), nil)
	e.Must(http.StatusForbidden, e.Call(tok.Token, "PUT", "/api/v1/kpis/otif_pct/goal", map[string]any{"target": 1}), nil)
	e.Must(http.StatusForbidden, e.Call(tok.Token, "DELETE", "/api/v1/kpis/otif_pct/goal", nil), nil)
	e.Must(http.StatusNoContent, e.Do("DELETE", "/api/v1/kpis/otif_pct/goal", nil), nil)
	if k := loadKPIs(t, e, "").get(t, "otif_pct"); k.Goal != nil || k.GoalStatus != "" {
		t.Errorf("goal removed: %+v", k)
	}
	var audit struct {
		Items []struct{ Action, Entity, EntityID string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?limit=200", nil), &audit)
	var sets, removes int
	for _, a := range audit.Items {
		if a.Entity == "kpi" && a.EntityID == "otif_pct" && a.Action == "kpi.goal.set" {
			sets++
		}
		if a.Entity == "kpi" && a.EntityID == "otif_pct" && a.Action == "kpi.goal.remove" {
			removes++
		}
	}
	if sets != 2 || removes != 1 {
		t.Errorf("audit entries: %d sets, %d removes in %+v", sets, removes, audit.Items)
	}

	// --- tenant isolation: another tenant sees none of this ---
	_, other := e.NewTenant("globex", "admin@globex.test")
	var ol kpiList
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/kpis", nil), &ol)
	for id, v := range map[string]float64{"alerts_open": 0, "cases_open": 0, "sla_breaches": 0} {
		want(t, ol.get(t, id), v)
	}
	if ol.get(t, "otif_pct").Value != nil || ol.get(t, "report_first_pass_pct").Value != nil {
		t.Error("another tenant has nothing to measure")
	}
	if ol.get(t, "serial_pool_utilisation_pct").Value != nil {
		t.Error("no pools, no utilisation")
	}
}

// TestKPILibraryPeerBenchmarks adds library KPIs to the benchmark dashboard only on request and
// only publishes peer figures with enough peers.
func TestKPILibraryPeerBenchmarks(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	now := time.Now().UTC()
	resolve := func(tenant string, hours int) {
		if _, err := conn.Exec(ctx, `insert into alert (id, tenant_id, kind, title, status, created_at, resolved_at) values (gen_random_uuid(), $1, 'test', 't', 'resolved', $2, $3)`,
			tenant, now.Add(-time.Duration(hours+24)*time.Hour), now.Add(-24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	resolve(e.Tenant.ID.String(), 5)
	for i, h := range []int{1, 2, 3, 4, 5} {
		ten, _ := e.NewTenant(fmt.Sprintf("peer%d", i), fmt.Sprintf("a@peer%d.test", i))
		resolve(ten.ID.String(), h)
	}

	type bm struct {
		KPIs []struct {
			Key         string
			Category    string
			Yours       *float64
			PeerMedian  *float64
			Peers       int
			LowerBetter bool `json:"lowerIsBetter"`
		}
	}
	var plain, lib bm
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/benchmarks", nil), &plain)
	if len(plain.KPIs) != 3 {
		t.Fatalf("default benchmarks keep their 3 KPIs: %d", len(plain.KPIs))
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/benchmarks?kpis=library", nil), &lib)
	if len(lib.KPIs) <= 3 || lib.KPIs[0].Key != "capture_rejection_pct" || lib.KPIs[2].Key != "case_resolution_hours" {
		t.Fatalf("library benchmarks: %d KPIs", len(lib.KPIs))
	}
	seen := map[string]bool{}
	for _, k := range lib.KPIs {
		if seen[k.Key] {
			t.Errorf("duplicate benchmark key %s", k.Key)
		}
		seen[k.Key] = true
		if k.Key == "sla_breaches" || k.Key == "alerts_open" || k.Key == "events_captured" {
			t.Errorf("%s is a volume or snapshot and has no peer comparison", k.Key)
		}
	}
	found := false
	for _, k := range lib.KPIs {
		if k.Key == "alert_mttr_hours" {
			found = true
			if k.Peers != 5 || k.Yours == nil || *k.Yours != 5 || k.PeerMedian == nil || *k.PeerMedian != 3 || !k.LowerBetter || k.Category != "operations" {
				t.Errorf("alert_mttr_hours: %+v", k)
			}
		}
		if k.Key == "otif_pct" && (k.PeerMedian != nil || k.Peers != 0) {
			t.Errorf("no peers have OTIF data: %+v", k)
		}
	}
	if !found {
		t.Error("alert_mttr_hours missing from the library benchmarks")
	}
}

// TestOTIFFromDocumentsCreatedThroughTheAPI measures on-time-in-full from real B2B documents: the
// customer's X12 850 (with its requested delivery date) and EDIFACT receiving advices, imported
// through the link endpoints, and a KPI whose query is broken does not take the list down.
func TestOTIFFromDocumentsCreatedThroughTheAPI(t *testing.T) {
	e := testkit.New(t)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	d := func(days int) time.Time { return today.AddDate(0, 0, days) }
	buyer, seller := edi.Party{Name: "Globex", GLN: "0614141000005"}, edi.Party{Name: "Acme", GLN: "9506000000008"}
	po := func(n string, req time.Time, qty float64) {
		doc := edi.Document{Type: "order", Number: n, Date: d(-10), Buyer: buyer, Seller: seller, Dates: map[string]string{"delivery_requested": req.Format("2006-01-02")},
			Lines: []edi.Line{{GTIN: "09506000134352", Quantity: qty, UOM: "EA", UnitPrice: 1}}}
		if err := doc.Validate(); err != nil {
			t.Fatal(err)
		}
		e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "x12", "content": edi.X12(doc, edi.Envelope{})}), nil)
	}
	recadv := func(n, po string, received time.Time, qty float64, status string) {
		doc := edi.Document{Type: "receiving_advice", Number: n, Date: received, Buyer: buyer, Seller: seller, References: map[string]string{"po": po},
			Dates: map[string]string{"received": received.Format("2006-01-02")}, Lines: []edi.Line{{GTIN: "09506000134352", Quantity: qty, UOM: "EA", Status: status}}}
		if err := doc.Validate(); err != nil {
			t.Fatal(err)
		}
		e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents/import", map[string]any{"format": "edifact", "content": edi.EDIFACT(doc, edi.Envelope{})}), nil)
	}
	po("PO-A", d(-3), 100)
	recadv("RC-A", "PO-A", d(-4), 100, "accepted") // on time, in full
	po("PO-B", d(-3), 100)
	recadv("RC-B", "PO-B", d(-2), 100, "accepted") // late
	po("PO-C", d(-3), 100)
	recadv("RC-C", "PO-C", d(-5), 90, "accepted") // short
	po("PO-D", d(-3), 100)
	recadv("RC-D", "PO-D", d(-5), 100, "damaged") // all damaged: nothing accepted
	l := loadKPIs(t, e, "?category=delivery")
	want(t, l.get(t, "otif_pct"), 25)
	want(t, l.get(t, "on_time_pct"), 75)
	want(t, l.get(t, "in_full_pct"), 50)
	if l.get(t, "otif_pct").Samples != 4 {
		t.Errorf("samples: %d", l.get(t, "otif_pct").Samples)
	}

	// A KPI with a broken query is reported on its own.
	saved := ecosystem.Library
	t.Cleanup(func() { ecosystem.Library = saved })
	ecosystem.Library = append(append([]ecosystem.KPI{}, saved...), ecosystem.KPI{ID: "broken", Name: "Broken", Category: "delivery", Unit: "count", Direction: ecosystem.LowerBetter,
		Kind: ecosystem.KindSum, Description: "x", SQL: `select tenant_id, day from nowhere where $1 < $2`})
	l = loadKPIs(t, e, "?category=delivery")
	if b := l.get(t, "broken"); b.Error == "" || b.Value != nil {
		t.Errorf("broken KPI: %+v", b)
	}
	want(t, l.get(t, "otif_pct"), 25)
	e.Must(http.StatusInternalServerError, e.Do("GET", "/api/v1/kpis/broken/series", nil), nil)
}
