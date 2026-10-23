package eufmd_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	connector "github.com/openspoor/connectors-sdk"
	"github.com/openspoor/openspoor/internal/connectors/eufmd"
)

func TestHandleRejectionFixes(t *testing.T) {
	c := eufmd.New()
	for code, want := range map[string]struct {
		fix       string
		retryable bool
	}{
		"MOCK-PRODUCT-UNKNOWN":  {"product master data", true},
		"MOCK-SERIAL-DUPLICATE": {"already exist", false},
		"MOCK-BATCH-MISSING":    {"batch number", true},
		"MOCK-EXPIRY-INVALID":   {"Expiry must be a date", true},
		"MOCK-SERIAL-UNKNOWN":   {"never uploaded", true},
		"SOMETHING-ELSE":        {"Review the hub message", true},
	} {
		a := c.HandleRejection(context.Background(), connector.Rejection{Code: code, Message: "m", Payload: connector.Payload{Kind: "pack-data-upload"}})
		if !strings.Contains(a.SuggestedFix, want.fix) || a.Retryable != want.retryable || a.Reason != code+": m" || !strings.Contains(a.Title, "pack-data-upload") {
			t.Fatalf("%s: %+v", code, a)
		}
	}
}

func TestSubmitAndPollAgainstHub(t *testing.T) {
	var auth, idem, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, idem, path = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"), r.URL.Path
		switch r.URL.Path {
		case "/v1/packdata":
			_, _ = w.Write([]byte(`{"transactionId":"T1"}`))
		case "/v1/packstatus":
			_, _ = w.Write([]byte(`{}`))
		case "/v1/transactions/BAD":
			_, _ = w.Write([]byte(`<html>`))
		case "/v1/transactions/REJ":
			_, _ = w.Write([]byte(`{"status":"REJECTED","productCode":"G","batch":"B","errors":[{"code":"X","message":"y","serial":"S1"},{"code":"Z","message":"w"}]}`))
		case "/v1/transactions/WAIT":
			_, _ = w.Write([]byte(`{"status":"QUEUED"}`))
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	c := eufmd.New()
	ctx := context.Background()
	cfg := connector.Config{"baseUrl": srv.URL + "/", "apiKey": "k"}

	sub, err := c.Submit(ctx, cfg, connector.Payload{Kind: "pack-data-upload", Key: "idem-1", Body: []byte(`{}`)})
	if err != nil || sub.Ref != "T1" || auth != "Bearer k" || idem != "idem-1" || path != "/v1/packdata" {
		t.Fatalf("submit: %v %+v %q %q %q", err, sub, auth, idem, path)
	}
	if _, err := c.Submit(ctx, cfg, connector.Payload{Kind: "pack-status-update"}); err == nil || path != "/v1/packstatus" || !strings.Contains(err.Error(), "unexpected hub response") {
		t.Fatalf("status update without ack: %v %s", err, path)
	}
	if _, err := c.Submit(ctx, connector.Config{"baseUrl": srv.URL + "/fail"}, connector.Payload{}); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("hub error: %v", err)
	}
	if _, err := c.Submit(ctx, connector.Config{"baseUrl": "http://127.0.0.1:1"}, connector.Payload{}); err == nil {
		t.Fatal("unreachable hub must fail")
	}
	if _, err := c.Submit(ctx, connector.Config{"baseUrl": "http://[::1"}, connector.Payload{}); err == nil {
		t.Fatal("malformed url must fail")
	}

	st, err := c.PollStatus(ctx, cfg, connector.Submission{Ref: "REJ"})
	if err != nil || st.State != connector.Rejected || st.Rejection.Code != "X" || len(st.Rejection.Packs) != 1 || st.Rejection.Packs[0].Lot != "B" {
		t.Fatalf("rejected: %v %+v", err, st)
	}
	if st, _ := c.PollStatus(ctx, cfg, connector.Submission{Ref: "WAIT"}); st.State != connector.Pending {
		t.Fatalf("pending: %+v", st)
	}
	if _, err := c.PollStatus(ctx, cfg, connector.Submission{Ref: "BAD"}); err == nil {
		t.Fatal("non-JSON status must fail")
	}
	if _, err := c.PollStatus(ctx, connector.Config{"baseUrl": "http://127.0.0.1:1"}, connector.Submission{Ref: "x"}); err == nil {
		t.Fatal("unreachable hub must fail")
	}
}

func TestMapGroupsByBatch(t *testing.T) {
	c := eufmd.New()
	e := connector.Event{EventID: "e1", BizStep: "destroying", Packs: []connector.Pack{{GTIN: "G", Serial: "2", Lot: "A"}, {GTIN: "G", Serial: "1", Lot: "A"}, {GTIN: "G", Serial: "3", Lot: "B"}}}
	pl, err := c.Map(context.Background(), e)
	if err != nil || len(pl) != 2 || pl[0].Kind != "pack-status-update" || !strings.Contains(string(pl[0].Body), `"serials":["1","2"]`) || !strings.Contains(string(pl[0].Body), "DESTROYED") {
		t.Fatalf("map: %v %+v", err, pl)
	}
	if pl[0].Key == pl[1].Key {
		t.Fatal("idempotency keys must differ per batch")
	}
}
