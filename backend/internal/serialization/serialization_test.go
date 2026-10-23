package serialization_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/app"
	"github.com/openspoor/openspoor/internal/serialization"
	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/pkg/gs1"
)

const gtin = "09506000134352"

func product(t *testing.T, e *testkit.Env) string {
	t.Helper()
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Serialized", "companyPrefixLength": 7}), &p)
	return p.ID
}

func TestPoolCreationAndListing(t *testing.T) {
	e := testkit.New(t)
	pid := product(t, e)

	for name, body := range map[string]map[string]any{
		"no market":       {"productId": pid},
		"unknown product": {"productId": "00000000-0000-0000-0000-000000000001", "market": "US"},
		"bad mode":        {"productId": pid, "market": "US", "mode": "chaotic"},
		"tiny random":     {"productId": pid, "market": "US", "mode": "random", "alphabet": "01", "length": 8},
		"dup alphabet":    {"productId": pid, "market": "US", "alphabet": "AABCDEFGHIJ"},
		"too long":        {"productId": pid, "market": "US", "prefix": "PPPPPPPPPP", "length": 12},
	} {
		if r := e.Do("POST", "/api/v1/serial-pools", body); r.Status != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d %s", name, r.Status, r.Body)
		}
	}
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/serial-pools", "{"), nil)

	var rnd, seq serialization.Pool
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": pid, "market": " us "}), &rnd)
	if rnd.Mode != "random" || rnd.Alphabet != serialization.DefaultAlphabet || rnd.Length != 12 || rnd.LowWatermarkPct != 10 ||
		rnd.Market != "US" || rnd.GTIN != gtin || rnd.Status != "active" || rnd.Remaining != rnd.Capacity || rnd.Capacity <= 0 {
		t.Fatalf("random defaults: %+v", rnd)
	}
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": pid, "market": "US"}), nil)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": pid, "market": "US", "mode": "sequential", "length": 6, "prefix": "S"}), &seq)
	if seq.Alphabet != "0123456789" || seq.Capacity != 1_000_000 {
		t.Fatalf("sequential defaults: %+v", seq)
	}

	var list struct {
		Items []serialization.Pool
		Total int
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/serial-pools", nil), &list)
	if list.Total != 2 || list.Items[0].ID != seq.ID {
		t.Fatalf("list: %+v", list)
	}
	var got serialization.Pool
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/serial-pools/"+rnd.ID.String(), nil), &got)
	if got.ID != rnd.ID || got.Remaining != rnd.Capacity {
		t.Fatalf("get: %+v", got)
	}
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/serial-pools/00000000-0000-0000-0000-000000000001", nil), nil)
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/serial-pools/x", nil), nil)

	// Pools are invisible to other tenants.
	_, tok := e.NewTenant("globex", "admin@globex.test")
	e.Must(http.StatusNotFound, e.Call(tok, "GET", "/api/v1/serial-pools/"+rnd.ID.String(), nil), nil)
}

func TestSequentialAllocationExhaustionAndLowAlert(t *testing.T) {
	e := testkit.New(t)
	pid := product(t, e)
	var pool serialization.Pool
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": pid, "market": "EU", "mode": "sequential", "length": 4, "prefix": "X"}), &pool)
	path := "/api/v1/serial-pools/" + pool.ID.String() + "/allocate"

	for name, tc := range map[string]struct {
		body any
		key  string
	}{
		"zero":       {map[string]int{"quantity": 0}, "k"},
		"too many":   {map[string]int{"quantity": serialization.MaxAllocation + 1}, "k"},
		"no key":     {map[string]int{"quantity": 1}, ""},
		"long key":   {map[string]int{"quantity": 1}, strings.Repeat("k", 201)},
		"bad body":   {"{", "k"},
		"bad poolid": {map[string]int{"quantity": 1}, "k"},
	} {
		p := path
		if name == "bad poolid" {
			p = "/api/v1/serial-pools/x/allocate"
		}
		if r := e.Do("POST", p, tc.body, "Idempotency-Key", tc.key); r.Status != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d %s", name, r.Status, r.Body)
		}
	}
	e.Must(http.StatusNotFound, e.Do("POST", "/api/v1/serial-pools/00000000-0000-0000-0000-000000000001/allocate", map[string]int{"quantity": 1}, "Idempotency-Key", "k"), nil)

	var a serialization.Allocation
	e.Must(http.StatusCreated, e.Do("POST", path, map[string]int{"quantity": 3}, "Idempotency-Key", "first"), &a)
	if a.Start != 0 || a.Count != 3 || a.Replayed || strings.Join(a.Serials, ",") != "X0000,X0001,X0002" {
		t.Fatalf("sequential serials: %+v", a)
	}
	var replay serialization.Allocation
	e.Must(http.StatusOK, e.Do("POST", path, map[string]int{"quantity": 3}, "Idempotency-Key", "first"), &replay)
	if !replay.Replayed || replay.AllocationID != a.AllocationID || strings.Join(replay.Serials, ",") != strings.Join(a.Serials, ",") {
		t.Fatalf("replay: %+v", replay)
	}

	// Crossing the 10% low watermark (capacity 10,000) raises one alert.
	var big serialization.Allocation
	e.Must(http.StatusCreated, e.Do("POST", path, map[string]int{"quantity": 9500}, "Idempotency-Key", "big"), &big)
	if big.Start != 3 || len(big.Serials) != 9500 || big.Serials[9499] != "X9502" {
		t.Fatalf("big allocation: start=%d n=%d last=%s", big.Start, len(big.Serials), big.Serials[len(big.Serials)-1])
	}
	e.Must(http.StatusCreated, e.Do("POST", path, map[string]int{"quantity": 1}, "Idempotency-Key", "small"), nil)
	var alerts struct {
		Items []struct{ Kind, Title string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=serialization.", nil), &alerts)
	if len(alerts.Items) != 1 || alerts.Items[0].Kind != "serialization.pool_low" || !strings.Contains(alerts.Items[0].Title, "497 left") {
		t.Fatalf("low alert (deduplicated): %+v", alerts.Items)
	}

	// Exhaustion: the counter never wraps and never reissues.
	e.Must(http.StatusConflict, e.Do("POST", path, map[string]int{"quantity": 497}, "Idempotency-Key", "over"), nil)
	var last serialization.Allocation
	e.Must(http.StatusCreated, e.Do("POST", path, map[string]int{"quantity": 496}, "Idempotency-Key", "rest"), &last)
	if last.Serials[len(last.Serials)-1] != "X9999" {
		t.Fatalf("last serial: %s", last.Serials[len(last.Serials)-1])
	}
	e.Must(http.StatusConflict, e.Do("POST", path, map[string]int{"quantity": 1}, "Idempotency-Key", "empty"), nil)
	var p serialization.Pool
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/serial-pools/"+pool.ID.String(), nil), &p)
	if p.Remaining != 0 || p.Issued != 10_000 {
		t.Fatalf("pool after exhaustion: %+v", p)
	}

	// A retired pool refuses allocations.
	var other serialization.Pool
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": pid, "market": "SA"}), &other)
	err := e.App.Deps.DB.OperatorTx(context.Background(), func(tx pgx.Tx) error {
		ct, err := tx.Exec(context.Background(), `update serial_pool set status='retired' where id=$1`, other.ID)
		if err == nil && ct.RowsAffected() != 1 {
			err = fmt.Errorf("retired %d pools", ct.RowsAffected())
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r := e.Do("POST", "/api/v1/serial-pools/"+other.ID.String()+"/allocate", map[string]int{"quantity": 1}, "Idempotency-Key", "retired")
	if r.Status != http.StatusConflict || !strings.Contains(string(r.Body), "pool is retired") {
		t.Fatalf("retired pool: %d %s", r.Status, r.Body)
	}
}

// A second app instance on the same database has no cached generator and must
// unseal the pool key from storage to reproduce the same serials.
func TestGeneratorRestoredFromSealedKey(t *testing.T) {
	e := testkit.New(t)
	pid := product(t, e)
	var pool serialization.Pool
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": pid, "market": "EU"}), &pool)
	path := "/api/v1/serial-pools/" + pool.ID.String() + "/allocate"
	var a serialization.Allocation
	e.Must(http.StatusCreated, e.Do("POST", path, map[string]int{"quantity": 4}, "Idempotency-Key", "k1"), &a)

	a2, err := app.New(context.Background(), e.App.Deps.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	srv := httptest.NewServer(a2.Router)
	defer srv.Close()
	other := &testkit.Env{T: t, App: a2, Server: srv, Token: e.Token, Tenant: e.Tenant}
	var replay serialization.Allocation
	other.Must(http.StatusOK, other.Do("POST", path, map[string]int{"quantity": 4}, "Idempotency-Key", "k1"), &replay)
	if strings.Join(replay.Serials, ",") != strings.Join(a.Serials, ",") {
		t.Fatalf("restored generator differs: %v vs %v", replay.Serials, a.Serials)
	}
	var next serialization.Allocation
	other.Must(http.StatusCreated, other.Do("POST", path, map[string]int{"quantity": 2}, "Idempotency-Key", "k2"), &next)
	for _, s := range next.Serials {
		for _, old := range a.Serials {
			if s == old {
				t.Fatalf("serial %s reissued", s)
			}
		}
	}
}

func TestEncodeDecode(t *testing.T) {
	e := testkit.New(t)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Prefix 8", "companyPrefixLength": 8}), nil)

	var cr serialization.CodeResult
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/codes/encode", map[string]any{"gtin": "9506000134352", "serial": "S1", "lot": "L1", "expiry": "301200", "format": "text"}), &cr)
	if cr.GTIN != gtin || cr.Image != "" || !strings.Contains(cr.ElementString, "<GS>") || !strings.HasPrefix(cr.HRI, "(01)"+gtin) ||
		!strings.HasPrefix(cr.DigitalLink, "http://localhost/01/"+gtin) {
		t.Fatalf("encode text: %+v", cr)
	}
	// The URN uses the product's company prefix length from master data.
	want, _ := gs1.SGTINURN(gtin, "S1", 8)
	if cr.URN != want {
		t.Fatalf("urn %s, want %s", cr.URN, want)
	}
	var lotOnly serialization.CodeResult
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/codes/encode", map[string]any{"gtin": "0614141000012", "lot": "L"}), &lotOnly)
	if cr = lotOnly; cr.URN != "" || !strings.HasPrefix(cr.Image, "data:image/png;base64,") {
		t.Fatalf("lot-only encode: %+v", cr)
	}

	for name, body := range map[string]any{
		"bad gtin":   map[string]any{"gtin": "124"},
		"long lot":   map[string]any{"gtin": gtin, "lot": strings.Repeat("L", 21)},
		"long ser":   map[string]any{"gtin": gtin, "serial": strings.Repeat("S", 21)},
		"bad expiry": map[string]any{"gtin": gtin, "expiry": "2030-01-01"},
		"bad json":   "{",
	} {
		if r := e.Do("POST", "/api/v1/codes/encode", body); r.Status != http.StatusBadRequest {
			t.Errorf("encode %s: HTTP %d %s", name, r.Status, r.Body)
		}
	}

	urn, _ := gs1.SGTINURN(gtin, "ABC", 7)
	for name, code := range map[string]string{
		"element {GS}": "01" + gtin + "21ABC{GS}10L1",
		"element <GS>": "  01" + gtin + "21ABC<GS>10L1  ",
		"digital link": "https://brand.example/01/" + gtin + "/21/ABC?10=L1",
		"urn":          urn,
	} {
		var d serialization.CodeResult
		e.Must(http.StatusOK, e.Do("POST", "/api/v1/codes/decode", map[string]any{"code": code}), &d)
		if d.GTIN != gtin || d.Serial != "ABC" {
			t.Errorf("decode %s: %+v", name, d)
		}
	}
	for name, code := range map[string]string{"garbage": "hello", "bad urn": "urn:epc:id:sgtin:xyz", "bad dl": "https://brand.example/zz/1", "empty": ""} {
		if r := e.Do("POST", "/api/v1/codes/decode", map[string]any{"code": code}); r.Status != http.StatusBadRequest {
			t.Errorf("decode %s: HTTP %d %s", name, r.Status, r.Body)
		}
	}
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/codes/decode", "{"), nil)
}

func TestParseCode(t *testing.T) {
	p, err := serialization.ParseCode("(01)" + gtin + "(21)XYZ(17)301231")
	if err != nil {
		t.Fatalf("HRI text: %v", err)
	}
	if p.GTIN != gtin || p.Serial != "XYZ" || p.Expiry != "301231" {
		t.Fatalf("hri: %+v", p)
	}
}

func TestCheckerStatuses(t *testing.T) {
	e := testkit.New(t)
	pid := product(t, e)
	var pool serialization.Pool
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": pid, "market": "EU", "mode": "sequential", "length": 6}), &pool)
	var a serialization.Allocation
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools/"+pool.ID.String()+"/allocate", map[string]int{"quantity": 3}, "Idempotency-Key", "c"), &a)

	var mod *serialization.Module
	for _, m := range e.App.Modules {
		if x, ok := m.(*serialization.Module); ok {
			mod = x
		}
	}
	if mod == nil || mod.Name() != "serialization" {
		t.Fatal("serialization module not registered")
	}
	ctx := context.Background()
	check := func(fn func(c *serialization.Checker, tx pgx.Tx)) {
		t.Helper()
		if err := e.App.Deps.DB.Tx(ctx, e.Tenant.ID, func(tx pgx.Tx) error {
			c, err := mod.NewChecker(ctx, tx, []string{gtin, "00614141000012"})
			if err != nil {
				return err
			}
			fn(c, tx)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	check(func(c *serialization.Checker, _ pgx.Tx) {
		got := c.StatusMany([]string{gtin, gtin, gtin, "00614141000012"}, []string{"000001", "000003", "ABC", "000001"})
		want := []serialization.SerialStatus{serialization.StatusAllocated, serialization.StatusUnallocated, serialization.StatusUnallocated, serialization.StatusUnmanaged}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("status %d: %s, want %s", i, got[i], want[i])
			}
		}
		dup, err := c.MarkCommissioned(ctx, gtin, []string{"000000", "000001", "000009"})
		if err != nil || len(dup) != 0 {
			t.Fatalf("first commission: %v %v", dup, err)
		}
	})
	// Bitmaps persist: a fresh checker sees the commissioned serials.
	check(func(c *serialization.Checker, _ pgx.Tx) {
		if st, _, n := c.Status(gtin, "000001"); st != serialization.StatusCommitted || n != 1 {
			t.Fatalf("after commit: %s %d", st, n)
		}
		dup, err := c.MarkCommissioned(ctx, gtin, []string{"000000", "000002"})
		if err != nil || len(dup) != 1 || dup[0] != "000000" {
			t.Fatalf("double commission: %v %v", dup, err)
		}
	})
	if err := e.App.Deps.DB.Tx(ctx, e.Tenant.ID, func(tx pgx.Tx) error {
		c, err := mod.NewChecker(ctx, tx, nil)
		if err != nil {
			return err
		}
		if st, _, _ := c.Status(gtin, "000001"); st != serialization.StatusUnmanaged {
			t.Errorf("empty checker: %s", st)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
