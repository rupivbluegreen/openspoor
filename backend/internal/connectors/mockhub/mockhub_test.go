package mockhub_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/connectors/mockhub"
)

func do(t *testing.T, c *http.Client, method, url, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var v map[string]any
	b, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(b, &v)
	return res.StatusCode, v
}

func upload(gtin, batch, exp string, serials ...string) string {
	b, _ := json.Marshal(map[string]any{"productCode": gtin, "batch": batch, "expiry": exp, "serials": serials})
	return string(b)
}

// poll returns the settled transaction (the first poll is always pending).
func poll(t *testing.T, c *http.Client, id string) map[string]any {
	t.Helper()
	_, v := do(t, c, "GET", "http://"+mockhub.EUHost+"/v1/transactions/"+id, "")
	if v["status"] != "PENDING" {
		t.Fatalf("first poll must be pending: %v", v)
	}
	_, v = do(t, c, "GET", "http://"+mockhub.EUHost+"/v1/transactions/"+id, "")
	return v
}

func firstCode(v map[string]any) string {
	errs, _ := v["errors"].([]any)
	if len(errs) == 0 {
		return ""
	}
	return errs[0].(map[string]any)["code"].(string)
}

func TestEUHubRules(t *testing.T) {
	c := &http.Client{Transport: mockhub.NewTransport()}
	base := "http://" + mockhub.EUHost
	future := time.Now().AddDate(1, 0, 0).Format("2006-01-02")
	g := "09506000134352"

	cases := []struct {
		name, body, code string
	}{
		{"unknown product", upload(g, "B1", future, "S1"), "MOCK-PRODUCT-UNKNOWN"},
	}
	for _, cs := range cases {
		_, v := do(t, c, "POST", base+"/v1/packdata", cs.body)
		if got := firstCode(poll(t, c, v["transactionId"].(string))); got != cs.code {
			t.Fatalf("%s: %s", cs.name, got)
		}
	}
	st, _ := do(t, c, "POST", base+"/v1/products", `{"productCode":"`+g+`"}`)
	if st != http.StatusCreated {
		t.Fatalf("register: %d", st)
	}
	for _, cs := range []struct{ name, body, code, status string }{
		{"missing batch", upload(g, "", future, "S1"), "MOCK-BATCH-MISSING", "REJECTED"},
		{"past expiry", upload(g, "B1", "2001-01-01", "S1"), "MOCK-EXPIRY-INVALID", "REJECTED"},
		{"malformed expiry", upload(g, "B1", "tomorrow", "S1"), "MOCK-EXPIRY-INVALID", "REJECTED"},
		{"serial too long", upload(g, "B1", future, strings.Repeat("9", 21)), "MOCK-SERIAL-TOO-LONG", "REJECTED"},
		{"ok", upload(g, "B1", future, "S1", "S2"), "", "PROCESSED"},
		{"duplicate", upload(g, "B1", future, "S2"), "MOCK-SERIAL-DUPLICATE", "REJECTED"},
	} {
		_, v := do(t, c, "POST", base+"/v1/packdata", cs.body)
		tx := poll(t, c, v["transactionId"].(string))
		if firstCode(tx) != cs.code || tx["status"] != cs.status {
			t.Fatalf("%s: %v", cs.name, tx)
		}
	}
	if st, _ := do(t, c, "POST", base+"/v1/packdata", "{bad"); st != http.StatusBadRequest {
		t.Fatalf("bad json: %d", st)
	}

	// Status updates only for uploaded packs; state is queryable.
	_, v := do(t, c, "POST", base+"/v1/packstatus", `{"productCode":"`+g+`","batch":"B1","newState":"SUPPLIED","serials":["S1"]}`)
	if tx := poll(t, c, v["transactionId"].(string)); tx["status"] != "PROCESSED" {
		t.Fatalf("status update: %v", tx)
	}
	_, v = do(t, c, "GET", base+"/v1/packs?productCode="+g+"&serial=S1", "")
	if v["state"] != "SUPPLIED" {
		t.Fatalf("pack state: %v", v)
	}
	_, v = do(t, c, "POST", base+"/v1/packstatus", `{"productCode":"`+g+`","serials":["NOPE"]}`)
	if tx := poll(t, c, v["transactionId"].(string)); firstCode(tx) != "MOCK-SERIAL-UNKNOWN" {
		t.Fatalf("unknown serial: %v", tx)
	}
	if st, _ := do(t, c, "GET", base+"/v1/transactions/EUTX-999999", ""); st != http.StatusNotFound {
		t.Fatalf("unknown txn: %d", st)
	}
	if st, _ := do(t, c, "DELETE", base+"/v1/products", ""); st != http.StatusNotFound {
		t.Fatalf("unknown route: %d", st)
	}
}

func TestSAHubRules(t *testing.T) {
	c := &http.Client{Transport: mockhub.NewTransport()}
	base := "http://" + mockhub.SAHost
	send := func(body map[string]any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		st, v := do(t, c, "POST", base+"/notifications", string(b))
		if st != http.StatusOK {
			t.Fatalf("notify: %d", st)
		}
		_, res := do(t, c, "GET", base+"/notifications/"+v["notificationId"].(string), "")
		return res
	}
	prods := []map[string]any{{"gtin": "09506000134352", "batch": "L1", "serials": []string{"A", "B"}}}
	if r := send(map[string]any{"notificationType": "ACCEPT", "products": prods}); r["reasonCode"] != "MOCK-NOT-PRODUCED" || len(r["failedSerials"].([]any)) != 2 {
		t.Fatalf("accept before production: %v", r)
	}
	if r := send(map[string]any{"notificationType": "PRODUCTION", "products": prods}); r["result"] != "SUCCESS" {
		t.Fatalf("production: %v", r)
	}
	if r := send(map[string]any{"notificationType": "DISPATCH", "products": prods}); r["reasonCode"] != "MOCK-RECEIVER-GLN-MISSING" {
		t.Fatalf("dispatch without gln: %v", r)
	}
	if r := send(map[string]any{"notificationType": "DISPATCH", "toGln": "6281000000003", "products": prods}); r["result"] != "SUCCESS" {
		t.Fatalf("dispatch: %v", r)
	}
	if st, _ := do(t, c, "POST", base+"/notifications", "nope"); st != http.StatusBadRequest {
		t.Fatalf("bad json: %d", st)
	}
	if st, _ := do(t, c, "GET", base+"/notifications/RSD-999", ""); st != http.StatusNotFound {
		t.Fatalf("unknown notification: %d", st)
	}
	if st, _ := do(t, c, "PUT", base+"/other", ""); st != http.StatusNotFound {
		t.Fatalf("unknown route: %d", st)
	}
}

type recordingRT struct{ hosts []string }

func (r *recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.hosts = append(r.hosts, req.URL.Host)
	return &http.Response{StatusCode: http.StatusTeapot, Body: io.NopCloser(bytes.NewReader(nil)), Request: req}, nil
}

func TestTransportFallsThroughToBase(t *testing.T) {
	rt := &recordingRT{}
	mt := mockhub.NewTransport()
	mt.Base = rt
	c := &http.Client{Transport: mt}
	res, err := c.Get("http://example.invalid/x")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusTeapot || len(rt.hosts) != 1 || rt.hosts[0] != "example.invalid" {
		t.Fatalf("base not used: %d %v", res.StatusCode, rt.hosts)
	}
	// Without a base, the default transport is used for real hosts.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	res, err = (&http.Client{Transport: mockhub.NewTransport()}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("default transport: %d", res.StatusCode)
	}
	// RegisterEUProduct is a no-op when the EU hub is not mounted.
	(&mockhub.Transport{Handlers: map[string]http.Handler{}}).RegisterEUProduct("x")
}
