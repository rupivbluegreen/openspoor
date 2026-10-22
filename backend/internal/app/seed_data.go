package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"time"

	"github.com/openspoor/openspoor/internal/platform"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// seeder drives the real API in-process, so demo data passes every business rule,
// lands in the audit trail and respects row-level security exactly like user traffic.
type seeder struct {
	a   *App
	ctx context.Context
	err error
}

func (s *seeder) call(token, method, path string, body any, out any, headers ...string) int {
	if s.err != nil {
		return 0
	}
	var buf []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		buf = b
	default:
		buf, _ = json.Marshal(b)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(buf)).WithContext(s.ctx)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	s.a.Router.ServeHTTP(rec, req)
	if rec.Code >= 300 {
		s.err = fmt.Errorf("seed %s %s: %d %s", method, path, rec.Code, truncate(rec.Body.String(), 400))
		return rec.Code
	}
	if out != nil && rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), out)
	}
	return rec.Code
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *seeder) drain() {
	if s.err == nil {
		s.err = s.a.Deps.Jobs.Drain(s.ctx)
	}
}

type company struct {
	slug, name, kind, gln, email string
	verticals                    []string
	token                        string
}

const cp = "9506000" // GS1 company prefix used by the fictional manufacturer

func gtin(item string) string { return gs1.WithCheckDigit("0" + cp + item) }

func sscc(n int) string { return gs1.WithCheckDigit("0" + cp + fmt.Sprintf("%09d", n)) }

// seedDemo loads three fictional companies — a manufacturer, a wholesaler and a pharmacy —
// connected as partners, with `packs` serialized packs, aggregation, shipments with TI/TS,
// VRS, an EU FMD connector with an accepted and a rejected report, a small food chain and
// agent proposals. It is idempotent: an existing demo is left as is.
func seedDemo(ctx context.Context, a *App, packs int, password string) error {
	if packs < 100 {
		packs = 100
	}
	if _, err := a.Platform.TenantBySlug(ctx, "nordwind"); err == nil {
		slog.Info("demo data already present")
		return nil
	}
	s := &seeder{a: a, ctx: ctx}
	cos := []*company{
		{"nordwind", "Nordwind Pharma (fictional)", "manufacturer", gs1.WithCheckDigit(cp + "00001"), "admin@nordwind.example", []string{"pharma"}, ""},
		{"harbor", "Harbor Distribution (fictional)", "wholesaler", gs1.WithCheckDigit("061414100000"), "admin@harbor.example", []string{"pharma", "food"}, ""},
		{"tulip", "Tulip Pharmacy (fictional)", "pharmacy", gs1.WithCheckDigit("871234500000"), "admin@tulip.example", []string{"pharma"}, ""},
	}
	for _, c := range cos {
		if _, _, err := a.Platform.CreateTenant(ctx, platform.NewTenant{Slug: c.slug, Name: c.name, AdminEmail: c.email, AdminPassword: password, Verticals: c.verticals, Plan: "enterprise"}); err != nil {
			return err
		}
		var tok struct{ Token string }
		s.call("", "POST", "/auth/login", map[string]string{"tenant": c.slug, "email": c.email, "password": password}, &tok)
		c.token = tok.Token
		var org struct{ ID string }
		s.call(c.token, "POST", "/orgs", map[string]any{"name": c.name, "kind": c.kind, "country": "NL", "companyPrefix": cp}, &org)
		s.call(c.token, "POST", "/sites", map[string]any{"orgId": org.ID, "gln": c.gln, "name": c.name + " main site", "kind": map[string]string{"manufacturer": "plant", "wholesaler": "warehouse", "pharmacy": "pharmacy"}[c.kind]}, nil)
		s.call(c.token, "PUT", "/partners/listing", map[string]any{"name": c.name, "gln": c.gln, "kind": c.kind, "country": "NL", "published": true,
			"description": "Fictional company created by the Open Spoor demo seed."}, nil)
	}
	if _, _, err := a.Platform.CreateTenant(ctx, platform.NewTenant{Slug: "platform", Name: "Platform operator", AdminEmail: "ops@openspoor.example", AdminPassword: password, Operator: true}); err != nil {
		return err
	}
	nw, hb, tl := cos[0], cos[1], cos[2]

	// Partnerships: Nordwind ↔ Harbor ↔ Tulip.
	connect := func(from, to *company) string {
		var inv struct{ Code string }
		s.call(from.token, "POST", "/partners/invitations", map[string]any{"email": to.email, "name": to.name}, &inv)
		s.call(to.token, "POST", "/partners/invitations/accept", map[string]any{"code": inv.Code}, nil)
		var ps struct{ Items []struct{ ID, Name string } }
		s.call(from.token, "GET", "/partners?status=pending", nil, &ps)
		id := ""
		for _, p := range ps.Items {
			id = p.ID
			s.call(from.token, "POST", "/partners/"+p.ID+"/approve", nil, nil)
		}
		return id
	}
	nwToHarbor := connect(nw, hb)
	hbToTulip := connect(hb, tl)

	// Products and pools.
	type prod struct {
		gtin, name, ndc, form, strength, size string
		markets                               []string
	}
	products := []prod{
		{gtin("13435"), "Metforma 850 mg film-coated tablets", "50090-1234-01", "TABLET, FILM COATED", "850 mg", "30 tablets", []string{"US", "NL", "DE"}},
		{gtin("13436"), "Amoxiwind 500 mg capsules", "50090-2234-01", "CAPSULE", "500 mg", "21 capsules", []string{"NL", "DE", "SA"}},
		{gtin("13437"), "Insulind 100 U/ml pen", "50090-3234-01", "INJECTION, SOLUTION", "100 U/ml", "5 x 3 ml", []string{"US", "NL"}},
	}
	type poolInfo struct{ id, gtin string }
	var pools []poolInfo
	for _, p := range products {
		var pr struct{ ID string }
		s.call(nw.token, "POST", "/products", map[string]any{"gtin": p.gtin, "name": p.name, "ndc": p.ndc, "dosageForm": p.form, "strength": p.strength, "packSize": p.size,
			"targetMarkets": p.markets, "companyPrefixLength": 7}, &pr)
		s.call(nw.token, "PUT", "/products/"+pr.ID+"/packaging/EU", map[string]any{"levels": []map[string]any{{"level": "unit", "quantity": 1}, {"level": "case", "quantity": 50}, {"level": "pallet", "quantity": 20}}}, nil)
		var pl struct{ ID string }
		s.call(nw.token, "POST", "/serial-pools", map[string]any{"productId": pr.ID, "market": "EU", "mode": "random", "length": 12}, &pl)
		pools = append(pools, poolInfo{pl.ID, p.gtin})
	}
	if s.err != nil {
		return s.err
	}

	// EU FMD connector against the in-process mock hub: Metforma is registered at the hub,
	// Amoxiwind is not, so the inbox shows a rejection with a suggested fix.
	for _, m := range a.Modules {
		if r, ok := m.(interface{ RegisterDemoHubProduct(string) }); ok {
			r.RegisterDemoHubProduct(products[0].gtin)
		}
	}
	s.call(nw.token, "POST", "/connectors/instances", map[string]any{"connector": "eu-fmd-emvs", "name": "EU Hub (mock)"}, nil)

	// Serialize and commission packs, then pack cases of 50 and pallets of 20 cases.
	start := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	per := packs / len(pools)
	caseNo, palletNo := 1, 900000
	var pallets []string
	palletPacks := map[string]int{}
	for pi, pl := range pools {
		var al struct{ Serials []string }
		s.call(nw.token, "POST", "/serial-pools/"+pl.id+"/allocate", map[string]any{"quantity": per}, &al, "Idempotency-Key", "demo-"+pl.gtin)
		lot := fmt.Sprintf("NW%02d%03d", pi+1, 101)
		exp := start.AddDate(2, pi, 0).Format("2006-01-02")
		if pi == 1 {
			exp = start.AddDate(0, 0, -10).Format("2006-01-02") // one expired batch for the expiry agent
		}
		var events []lib.Event
		var epcs []string
		for _, ser := range al.Serials {
			u, _ := gs1.SGTINURN(pl.gtin, ser, 7)
			epcs = append(epcs, u)
		}
		for i := 0; i < len(epcs); i += 500 {
			end := min(i+500, len(epcs))
			events = append(events, lib.Event{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: "active",
				EventTime: start.Add(time.Duration(i) * time.Second), EventTimeZoneOffset: "+00:00", EPCList: epcs[i:end],
				ReadPoint: &lib.Location{ID: gs1.DefaultResolver + "/414/" + nw.gln}, ILMD: map[string]any{"lotNumber": lot, "itemExpirationDate": exp}})
		}
		var cases []string
		for i := 0; i+50 <= len(epcs); i += 50 {
			sc, _ := gs1.SSCCURN(sscc(caseNo), 7)
			caseNo++
			cases = append(cases, sc)
			events = append(events, lib.Event{Type: lib.AggregationEvent, Action: "ADD", BizStep: "packing", Disposition: "in_progress",
				EventTime: start.Add(2 * time.Hour), EventTimeZoneOffset: "+00:00", ParentID: sc, ChildEPCs: epcs[i : i+50],
				BizLocation: &lib.Location{ID: gs1.DefaultResolver + "/414/" + nw.gln}})
		}
		palletSize := 20
		if len(cases) < 40 {
			palletSize = max(1, len(cases)/2)
		}
		for i := 0; i+palletSize <= len(cases); i += palletSize {
			pal, _ := gs1.SSCCURN(sscc(palletNo), 7)
			palletNo++
			pallets = append(pallets, pal)
			palletPacks[pal] = palletSize * 50
			events = append(events, lib.Event{Type: lib.AggregationEvent, Action: "ADD", BizStep: "packing", EventTime: start.Add(3 * time.Hour), EventTimeZoneOffset: "+00:00",
				ParentID: pal, ChildEPCs: cases[i : i+palletSize], BizLocation: &lib.Location{ID: gs1.DefaultResolver + "/414/" + nw.gln}})
		}
		doc, _ := lib.WriteJSON(lib.NewDocument(events))
		s.call(nw.token, "POST", "/epcis/capture", doc, nil)
		s.drain()
	}
	if s.err != nil {
		return s.err
	}

	// Ship two pallets to Harbor with an ASN; Harbor ships one case on to Tulip.
	if len(pallets) >= 2 {
		var sh struct{ ID string }
		s.call(nw.token, "POST", "/shipments", map[string]any{"partnerId": nwToHarbor, "poNumber": "PO-HB-24001", "asnNumber": "ASN-NW-0001", "epcs": pallets[:2],
			"asn": map[string]any{"items": []map[string]any{{"gtin": products[0].gtin, "quantity": palletPacks[pallets[0]] + palletPacks[pallets[1]]}}}}, &sh)
		s.call(nw.token, "POST", "/shipments/"+sh.ID+"/send", nil, nil)
		s.drain()
		var tree struct{ Children []struct{ EPC string } }
		s.call(hb.token, "GET", "/trace/tree/?id="+pallets[0], nil, &tree)
		if len(tree.Children) > 0 {
			var sh2 struct{ ID string }
			s.call(hb.token, "POST", "/shipments", map[string]any{"partnerId": hbToTulip, "poNumber": "PO-TL-0042", "asnNumber": "ASN-HB-0042", "epcs": []string{tree.Children[0].EPC}}, &sh2)
			s.call(hb.token, "POST", "/shipments/"+sh2.ID+"/send", nil, nil)
			s.drain()
		}
	}
	// VRS lookup directory at the wholesaler points at the manufacturer on this node.
	nwTenant, _ := a.Platform.TenantBySlug(ctx, "nordwind")
	s.call(hb.token, "POST", "/vrs/directory", map[string]any{"gtinPrefix": cp, "responderUrl": "local:" + nwTenant.ID.String()}, nil)

	// A small food chain at Harbor (fresh produce for the pharmacy shop).
	s.call(hb.token, "POST", "/food/lots", map[string]any{"tlc": "HB-APL-2611", "description": "Organic apples", "quantity": 800, "uom": "KGM", "locationGln": hb.gln, "origin": "received"}, nil)
	s.call(hb.token, "POST", "/food/cte", map[string]any{"type": "shipping", "tlc": "HB-APL-2611", "quantity": 120, "uom": "KGM", "locationGln": hb.gln, "shipTo": tl.gln,
		"shipToName": tl.name, "refDocType": "BOL", "refDocNumber": "BOL-7781"}, nil)

	// Cold-chain readings with one excursion for the insulin pallet.
	base := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Minute)
	var rd []map[string]any
	for i, c := range []float64{5.1, 5.4, 6.0, 8.9, 9.6, 7.2, 5.5, 5.0} {
		rd = append(rd, map[string]any{"time": base.Add(time.Duration(i) * 15 * time.Minute), "tempC": c})
	}
	if len(pallets) > 2 {
		s.call(nw.token, "POST", "/sensors/readings", map[string]any{"deviceId": "logger-NW-17", "epc": pallets[len(pallets)-1], "readings": rd}, nil)
	}
	s.drain()
	// Give delayed jobs (hub status polls) a few seconds so the demo starts settled.
	for i := 0; i < 15 && s.err == nil; i++ {
		s.drain()
		time.Sleep(400 * time.Millisecond)
	}
	// Let the agents look at everything once.
	for _, ag := range []string{"exception-triage", "expiry-quarantine", "regulatory-resubmitter"} {
		s.call(nw.token, "POST", "/agents/"+ag+"/run", nil, nil)
	}
	s.drain()
	if s.err != nil {
		return s.err
	}
	slog.Info("demo data loaded", "packs", per*len(pools), "pallets", len(pallets), "logins", "admin@{nordwind,harbor,tulip}.example and ops@openspoor.example (tenant platform)")
	fmt.Printf("Demo loaded: %d packs. Sign in at tenant nordwind, harbor or tulip as admin@<tenant>.example, or as ops@openspoor.example (tenant platform), password %q.\n", per*len(pools), password)
	return nil
}
