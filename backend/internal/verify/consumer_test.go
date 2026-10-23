package verify_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/internal/verify"
	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

type fakeMail struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeMail) Send(_ context.Context, to, subject, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, to+"|"+subject+"|"+body)
	return nil
}

func TestConsumerPage(t *testing.T) {
	e := testkit.New(t)
	var mod *verify.Module
	for _, m := range e.App.Modules {
		if v, ok := m.(*verify.Module); ok {
			mod = v
		}
	}
	mailer := &fakeMail{}
	mod.Mail = mailer

	gtin := "09506000134352"
	var p struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/products", map[string]any{"gtin": gtin, "name": "Amoxicillin 500mg"}), &p)
	var pool struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools", map[string]any{"productId": p.ID, "market": "EU"}), &pool)
	var a struct{ Serials []string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/serial-pools/"+pool.ID+"/allocate", map[string]any{"quantity": 2}, "Idempotency-Key", "c"), &a)
	u0, _ := gs1.SGTINURN(gtin, a.Serials[0], 7)
	t0 := time.Now().Add(-time.Hour).UTC()
	comm := lib.Event{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: "active", EventTime: t0, EventTimeZoneOffset: "+00:00",
		EPCList: []string{u0}, ILMD: map[string]any{"lotNumber": "B12", "itemExpirationDate": "2031-06-30"}}
	b, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{comm}))
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), nil)
	e.Drain()
	link := gs1.DigitalLink("https://brand.example", gs1.Pack{GTIN: gtin, Serial: a.Serials[0]})

	// The brand owner publishes information in two languages; validation rejects unsafe links.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/consumer-info/"+gtin+"/en", map[string]any{"leafletUrl": "https://brand.example/leaflet-en.pdf",
		"allergens": []string{"lactose"}, "certificates": []map[string]any{{"name": "GMP", "issuer": "Inspectorate"}}}), nil)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/consumer-info/"+gtin+"/nl", map[string]any{"leafletUrl": "https://brand.example/bijsluiter.pdf"}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/consumer-info/"+gtin+"/en", map[string]any{"leafletUrl": "javascript:alert(1)"}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/consumer-info/"+gtin+"/english", map[string]any{}), nil)
	e.Must(http.StatusNotFound, e.Do("PUT", "/api/v1/consumer-info/09506000134369/en", map[string]any{}), nil)

	type scanRes struct {
		Verified     bool
		Status       string
		ExpiryDate   string
		RecallAlerts bool
		Pack         struct{ Lot string }
		Info         *struct {
			Lang       string
			LeafletURL string
			Allergens  []string
		}
	}
	var r scanRes
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/verify/scan", map[string]any{"code": link, "lang": "nl"}), &r)
	if !r.Verified || r.Info == nil || r.Info.Lang != "nl" || r.ExpiryDate != "2031-06-30" || r.Pack.Lot != "B12" || !r.RecallAlerts {
		t.Fatalf("scan in Dutch: %+v info=%+v", r, r.Info)
	}
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/verify/scan", map[string]any{"code": link, "lang": "ar"}), &r)
	if r.Info == nil || r.Info.Lang != "en" || len(r.Info.Allergens) != 1 {
		t.Fatalf("fallback to English: %+v", r.Info)
	}

	// Report a suspect product: an alert for the brand owner, no personal data.
	e.Must(http.StatusBadRequest, e.Call("", "POST", "/api/v1/verify/report", map[string]any{"code": link, "reason": "because"}), nil)
	e.Must(http.StatusAccepted, e.Call("", "POST", "/api/v1/verify/report", map[string]any{"code": link, "reason": "seal", "country": "nl"}), nil)
	var reports struct {
		Items []struct{ Reason, Country string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/verify/reports", nil), &reports)
	if len(reports.Items) != 1 || reports.Items[0].Reason != "seal" || reports.Items[0].Country != "NL" {
		t.Fatalf("reports: %+v", reports.Items)
	}

	// Recall notices: consent and a valid address are required.
	e.Must(http.StatusBadRequest, e.Call("", "POST", "/api/v1/verify/recall-alerts", map[string]any{"code": link, "email": "pat@example.com"}), nil)
	e.Must(http.StatusBadRequest, e.Call("", "POST", "/api/v1/verify/recall-alerts", map[string]any{"code": link, "email": "Pat <pat@example.com>", "consent": true}), nil)
	var sub struct{ Until string }
	e.Must(http.StatusAccepted, e.Call("", "POST", "/api/v1/verify/recall-alerts", map[string]any{"code": link, "email": "pat@example.com", "consent": true}), &sub)
	// Kept no longer than the pack's expiry (what the consent text says), and at most two years.
	want := time.Date(2031, 6, 30, 0, 0, 0, 0, time.UTC)
	if limit := time.Now().AddDate(2, 0, 0); limit.Before(want) {
		want = limit
	}
	if sub.Until != want.Format(time.DateOnly) {
		t.Fatalf("subscription kept until %s, want %s", sub.Until, want.Format(time.DateOnly))
	}
	e.Must(http.StatusAccepted, e.Call("", "POST", "/api/v1/verify/recall-alerts", map[string]any{"code": link, "email": "sam@example.com", "consent": true}), nil)

	// Nothing is sent before a recall.
	if err := mod.SendRecallNotices(t.Context()); err != nil || len(mailer.sent) != 0 {
		t.Fatalf("before recall: err=%v sent=%d", err, len(mailer.sent))
	}
	recall := lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "holding", Disposition: "recalled", EventTime: t0.Add(time.Minute), EventTimeZoneOffset: "+00:00", EPCList: []string{u0}}
	b, _ = lib.WriteJSON(lib.NewDocument([]lib.Event{recall}))
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), nil)
	e.Drain()
	if err := mod.SendRecallNotices(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(mailer.sent) != 2 || !strings.Contains(mailer.sent[0], "B12") || !strings.Contains(mailer.sent[0], "Amoxicillin 500mg") {
		t.Fatalf("recall notices: %v", mailer.sent)
	}
	// Each address is used once and then deleted.
	if err := mod.SendRecallNotices(t.Context()); err != nil || len(mailer.sent) != 2 {
		t.Fatalf("second run sent again: %d", len(mailer.sent))
	}

	// Anonymous statistics: outcomes counted, nothing about the scanner.
	var st struct {
		Items []struct {
			Outcome string
			Count   int
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/verify/stats", nil), &st)
	got := map[string]int{}
	for _, it := range st.Items {
		got[it.Outcome] += it.Count
	}
	if got["verified"] != 2 || got["reported"] != 1 { // two scans; opting in to notices is not a scan
		t.Fatalf("stats: %v", got)
	}
	// Public routes need no login; brand-owner routes do.
	e.Must(http.StatusUnauthorized, e.Call("", "GET", "/api/v1/verify/stats", nil), nil)
}

func TestUnsubscribeNeedsToken(t *testing.T) {
	e := testkit.New(t)
	e.Must(http.StatusBadRequest, e.Call("", "POST", "/api/v1/verify/recall-alerts/unsubscribe", map[string]any{}), nil)
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/verify/recall-alerts/unsubscribe", map[string]any{"token": "unknown"}), nil)
}
