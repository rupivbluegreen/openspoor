package sarsd_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	connector "github.com/openspoor/connectors-sdk"
	"github.com/openspoor/openspoor/internal/connectors/sarsd"
)

func TestHandleRejectionFixes(t *testing.T) {
	c := sarsd.New()
	for code, fix := range map[string]string{
		"MOCK-RECEIVER-GLN-MISSING": "receiver's GLN",
		"MOCK-NOT-PRODUCED":         "PRODUCTION notification first",
		"MOCK-GTIN-NOT-REGISTERED":  "Register the GTIN",
		"OTHER":                     "Review the RSD reason",
	} {
		a := c.HandleRejection(context.Background(), connector.Rejection{Code: code, Message: "m", Payload: connector.Payload{Kind: "dispatch"}})
		if !strings.Contains(a.SuggestedFix, fix) || !a.Retryable || !strings.Contains(a.Title, "DISPATCH") {
			t.Fatalf("%s: %+v", code, a)
		}
	}
}

func TestSubmitAndPoll(t *testing.T) {
	var user, pass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ = r.BasicAuth()
		switch r.URL.Path {
		case "/ok/notifications":
			_, _ = w.Write([]byte(`{"notificationId":"N1"}`))
		case "/noid/notifications":
			_, _ = w.Write([]byte(`{}`))
		case "/ok/notifications/FAILED":
			_, _ = w.Write([]byte(`{"result":"FAILED","reasonCode":"R","description":"d","failedSerials":[{"GTIN":"G","Serial":"S"}]}`))
		case "/ok/notifications/WAIT":
			_, _ = w.Write([]byte(`{"result":"IN_PROGRESS"}`))
		case "/ok/notifications/BAD":
			_, _ = w.Write([]byte(`not json`))
		default:
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	c := sarsd.New()
	ctx := context.Background()
	cfg := connector.Config{"baseUrl": srv.URL + "/ok/", "username": "u", "password": "p"}
	sub, err := c.Submit(ctx, cfg, connector.Payload{Key: "k", Body: []byte(`{}`)})
	if err != nil || sub.Ref != "N1" || user != "u" || pass != "p" {
		t.Fatalf("submit: %v %+v %s/%s", err, sub, user, pass)
	}
	if _, err := c.Submit(ctx, connector.Config{"baseUrl": srv.URL + "/noid"}, connector.Payload{}); err == nil || !strings.Contains(err.Error(), "unexpected RSD response") {
		t.Fatalf("no id: %v", err)
	}
	if _, err := c.Submit(ctx, connector.Config{"baseUrl": srv.URL + "/down"}, connector.Payload{}); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("down: %v", err)
	}
	if _, err := c.Submit(ctx, connector.Config{"baseUrl": "http://127.0.0.1:1"}, connector.Payload{}); err == nil {
		t.Fatal("unreachable must fail")
	}
	if _, err := c.Submit(ctx, connector.Config{"baseUrl": "http://[::1"}, connector.Payload{}); err == nil {
		t.Fatal("malformed url must fail")
	}
	st, err := c.PollStatus(ctx, cfg, connector.Submission{Ref: "FAILED"})
	if err != nil || st.State != connector.Rejected || st.Rejection.Code != "R" || len(st.Rejection.Packs) != 1 {
		t.Fatalf("failed: %v %+v", err, st)
	}
	if st, _ := c.PollStatus(ctx, cfg, connector.Submission{Ref: "WAIT"}); st.State != connector.Pending {
		t.Fatalf("pending: %+v", st)
	}
	if _, err := c.PollStatus(ctx, cfg, connector.Submission{Ref: "BAD"}); err == nil {
		t.Fatal("non-JSON must fail")
	}
	if _, err := c.PollStatus(ctx, connector.Config{"baseUrl": "http://127.0.0.1:1"}, connector.Submission{Ref: "x"}); err == nil {
		t.Fatal("unreachable must fail")
	}
}

func TestMapUsesLocationAsSender(t *testing.T) {
	c := sarsd.New()
	if pl, _ := c.Map(context.Background(), connector.Event{BizStep: "packing", Packs: []connector.Pack{{GTIN: "G"}}}); pl != nil {
		t.Fatal("untriggered step must map to nothing")
	}
	pl, err := c.Map(context.Background(), connector.Event{EventID: "e", BizStep: "receiving", Location: "6281000000003",
		Packs: []connector.Pack{{GTIN: "G", Serial: "b", Lot: "L"}, {GTIN: "G", Serial: "a", Lot: "L"}, {GTIN: "H", Serial: "c", Lot: "M"}}})
	if err != nil || len(pl) != 1 || pl[0].Kind != "accept" || !strings.Contains(string(pl[0].Body), `"fromGln":"6281000000003"`) || !strings.Contains(string(pl[0].Body), `"serials":["a","b"]`) {
		t.Fatalf("map: %v %+v", err, pl)
	}
}
