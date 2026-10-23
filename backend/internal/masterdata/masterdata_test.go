package masterdata_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/masterdata"
	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/pkg/gs1"
)

func TestProductCRUD(t *testing.T) {
	e := testkit.New(t)
	gtin := gs1.WithCheckDigit("0950600013437")

	var p masterdata.Product
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Amoxicillin 500mg",
		"targetMarkets": []string{" us ", "de"}, "ndc": "12345-678-90"}), &p)
	if p.Vertical != "pharma" || p.Status != "active" || p.CompanyPrefixLength != 7 || len(p.TargetMarkets) != 2 || p.TargetMarkets[0] != "US" {
		t.Fatalf("defaults not applied: %+v", p)
	}
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "dup"}), nil)

	for name, body := range map[string]map[string]any{
		"bad gtin":      {"gtin": "09506000134353", "name": "x"},
		"no name":       {"gtin": gtin, "name": "  "},
		"prefix length": {"gtin": gtin, "name": "x", "companyPrefixLength": 13},
		"vertical":      {"gtin": gtin, "name": "x", "vertical": "toys"},
	} {
		if r := e.Do("POST", "/api/v1/products", body); r.Status != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d %s", name, r.Status, r.Body)
		}
	}

	var got masterdata.Product
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/products/"+p.ID.String(), nil), &got)
	if got.GTIN != gtin || got.NDC == nil || *got.NDC != "12345-678-90" {
		t.Fatalf("get: %+v", got)
	}
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/products/00000000-0000-0000-0000-000000000001", nil), nil)
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/products/not-a-uuid", nil), nil)

	var up masterdata.Product
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/products/"+p.ID.String(), map[string]any{"gtin": gtin, "name": "Amoxicillin 250mg", "vertical": "device", "ndc": "12345-678-90"}), &up)
	if up.ID != p.ID || up.Name != "Amoxicillin 250mg" || up.Vertical != "device" || !up.UpdatedAt.After(p.UpdatedAt) && !up.UpdatedAt.Equal(p.UpdatedAt) {
		t.Fatalf("update: %+v", up)
	}
	other := gs1.WithCheckDigit("0950600013444")
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/products/"+p.ID.String(), map[string]any{"gtin": other, "name": "x"}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/products/"+p.ID.String(), map[string]any{"gtin": "1", "name": "x"}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/products/"+p.ID.String(), "{not json"), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/products/zzz", map[string]any{"gtin": gtin, "name": "x"}), nil)
	e.Must(http.StatusNotFound, e.Do("PUT", "/api/v1/products/00000000-0000-0000-0000-000000000001", map[string]any{"gtin": gtin, "name": "x"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/products", "{"), nil)

	// Search by name, GTIN prefix and NDC; paging.
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": other, "name": "Zinc tablets", "vertical": "food"}), nil)
	var page struct {
		Items []masterdata.Product
		Total int
	}
	for q, want := range map[string]int{"": 2, "amoxi": 1, "0950600013437": 1, "12345-678-90": 1, "nothing": 0} {
		e.Must(http.StatusOK, e.Do("GET", "/api/v1/products?q="+q, nil), &page)
		if page.Total != want || len(page.Items) != want {
			t.Errorf("q=%q: total=%d items=%d want %d", q, page.Total, len(page.Items), want)
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/products?limit=1&offset=1", nil), &page)
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].Name != "Zinc tablets" {
		t.Fatalf("paging: %+v", page)
	}

	// Lookups used by other modules.
	err := e.App.Deps.DB.Tx(context.Background(), e.Tenant.ID, func(tx pgx.Tx) error {
		byG, err := masterdata.ByGTIN(context.Background(), tx, gtin)
		if err != nil || byG.ID != p.ID {
			t.Errorf("ByGTIN: %+v %v", byG, err)
		}
		byID, err := masterdata.ByID(context.Background(), tx, p.ID)
		if err != nil || byID.GTIN != gtin {
			t.Errorf("ByID: %+v %v", byID, err)
		}
		known, err := masterdata.KnownGTINs(context.Background(), tx, []string{gtin, "00000000000000"})
		if err != nil || len(known) != 1 || known[gtin].ID != p.ID {
			t.Errorf("KnownGTINs: %v %v", known, err)
		}
		empty, err := masterdata.KnownGTINs(context.Background(), tx, nil)
		if err != nil || len(empty) != 0 {
			t.Errorf("KnownGTINs(nil): %v %v", empty, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Products are tenant-scoped.
	_, tok := e.NewTenant("globex", "admin@globex.test")
	e.Must(http.StatusNotFound, e.Call(tok, "GET", "/api/v1/products/"+p.ID.String(), nil), nil)
}

func TestImportCSV(t *testing.T) {
	e := testkit.New(t)
	g1 := gs1.WithCheckDigit("0950600013437")
	g2 := gs1.WithCheckDigit("0950600013444")
	g3 := gs1.WithCheckDigit("0950600013451")
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": g3, "name": "Old name"}), nil)

	csv := "\xef\xbb\xbfGTIN, Name ,ndc,company_prefix_length,target_markets,dosage_form,vertical\n" +
		g1 + ",Aspirin,1111-222-33,8,US;DE|fr,tablet,pharma\n" +
		g2 + ",Bandage,,,,,device\n" +
		g3 + ",New name,,,,,\n" +
		"124,Bad GTIN\n" +
		g1[:13] + "0,Bad check\n" +
		g2 + ",Bad prefix,,abc\n" +
		g2 + ",Bad vertical,,,,,toys\n" +
		g2 + ",\n" +
		"\"unterminated,x\n"
	var res masterdata.ImportResult
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/products/import", csv), &res)
	if res.Created != 2 || res.Updated != 1 {
		t.Fatalf("result: %+v", res)
	}
	wantLines := map[int]string{5: "gtin", 6: "gtin", 7: "company_prefix_length", 8: "vertical", 9: "name"}
	got := map[int]string{}
	for _, er := range res.Errors {
		got[er.Line] = er.Message
	}
	for line, frag := range wantLines {
		if !strings.Contains(got[line], frag) {
			t.Errorf("line %d: got %q, want mention of %q (all: %+v)", line, got[line], frag, res.Errors)
		}
	}
	if len(res.Errors) != 6 {
		t.Fatalf("want 6 row errors (incl. CSV parse error), got %+v", res.Errors)
	}

	var page struct{ Items []masterdata.Product }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/products?q="+g1, nil), &page)
	if len(page.Items) != 1 {
		t.Fatalf("imported product missing: %+v", page)
	}
	p := page.Items[0]
	if p.CompanyPrefixLength != 8 || p.NDC == nil || *p.NDC != "1111-222-33" || strings.Join(p.TargetMarkets, ",") != "US,DE,FR" || p.DosageForm != "tablet" {
		t.Fatalf("imported fields: %+v", p)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/products?q="+g3, nil), &page)
	if page.Items[0].Name != "New name" {
		t.Fatalf("upsert did not update: %+v", page.Items[0])
	}

	// Header problems reject the whole file.
	for name, body := range map[string]string{"empty": "", "no gtin": "name\nx\n", "no name": "gtin\n" + g1 + "\n"} {
		if r := e.Do("POST", "/api/v1/products/import", body); r.Status != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d %s", name, r.Status, r.Body)
		}
	}
}

func TestPackagingHierarchy(t *testing.T) {
	e := testkit.New(t)
	gtin := gs1.WithCheckDigit("0950600013437")
	var p masterdata.Product
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Pack"}), &p)
	base := "/api/v1/products/" + p.ID.String() + "/packaging"

	bad := map[string][]masterdata.Level{
		"empty":         {},
		"unknown level": {{Level: "crate", Quantity: 1}},
		"out of order":  {{Level: "case", Quantity: 1}, {Level: "unit", Quantity: 1}},
		"repeat":        {{Level: "unit"}, {Level: "unit", Quantity: 2}},
		"zero quantity": {{Level: "unit"}, {Level: "case", Quantity: 0}},
		"bad gtin":      {{Level: "unit", GTIN: "12345"}},
	}
	for name, lv := range bad {
		if r := e.Do("PUT", base+"/us", map[string]any{"levels": lv}); r.Status != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d %s", name, r.Status, r.Body)
		}
	}
	e.Must(http.StatusBadRequest, e.Do("PUT", base+"/us", "nope"), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/products/x/packaging/us", map[string]any{"levels": []masterdata.Level{{Level: "unit"}}}), nil)

	levels := []masterdata.Level{{Level: "unit", GTIN: gtin}, {Level: "case", GTIN: gs1.WithCheckDigit("1950600013437"), Quantity: 12}, {Level: "pallet", Quantity: 40}}
	var tpl masterdata.PackagingTemplate
	e.Must(http.StatusOK, e.Do("PUT", base+"/us", map[string]any{"levels": levels}), &tpl)
	if tpl.Market != "US" || tpl.ProductID != p.ID || len(tpl.Levels) != 3 || tpl.Levels[1].Quantity != 12 {
		t.Fatalf("put: %+v", tpl)
	}
	// Replacing keeps the same template.
	var tpl2 masterdata.PackagingTemplate
	e.Must(http.StatusOK, e.Do("PUT", base+"/US", map[string]any{"levels": levels[:2]}), &tpl2)
	if tpl2.ID != tpl.ID || len(tpl2.Levels) != 2 {
		t.Fatalf("replace: %+v", tpl2)
	}
	e.Must(http.StatusOK, e.Do("PUT", base+"/de", map[string]any{"levels": levels[:1]}), nil)

	var one masterdata.PackagingTemplate
	e.Must(http.StatusOK, e.Do("GET", base+"/us", nil), &one)
	if one.ID != tpl.ID || len(one.Levels) != 2 {
		t.Fatalf("get: %+v", one)
	}
	e.Must(http.StatusNotFound, e.Do("GET", base+"/fr", nil), nil)
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/products/x/packaging/us", nil), nil)

	var list struct {
		Items []masterdata.PackagingTemplate
		Total int
	}
	e.Must(http.StatusOK, e.Do("GET", base, nil), &list)
	if list.Total != 2 || list.Items[0].Market != "DE" || list.Items[1].Market != "US" {
		t.Fatalf("list: %+v", list)
	}
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/products/x/packaging", nil), nil)
	// Unknown product is a 404, not a database error.
	e.Must(http.StatusNotFound, e.Do("PUT", "/api/v1/products/00000000-0000-0000-0000-000000000001/packaging/us", map[string]any{"levels": levels[:1]}), nil)
	if (&masterdata.Module{}).Name() != "masterdata" {
		t.Fatal("module name")
	}
}
