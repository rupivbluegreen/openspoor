package hubjson_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	connector "github.com/openspoor/connectors-sdk"
	"github.com/openspoor/openspoor/internal/connectors/aetatmeen"
	"github.com/openspoor/openspoor/internal/connectors/egeda"
	"github.com/openspoor/openspoor/internal/connectors/hubjson"
)

func TestProfilesMarkUnknownsAsTodoSpec(t *testing.T) {
	for _, c := range []*hubjson.Connector{aetatmeen.New(), egeda.New()} {
		info := c.Info()
		if !strings.Contains(info.Spec, "TODO(spec)") || len(info.Triggers) != len(hubjson.ReportTypes) {
			t.Fatalf("%s: %+v", info.ID, info)
		}
		for _, f := range info.Config {
			if f.Key != "participantId" && f.Key != "baseUrl" && f.Key != "clientId" && f.Key != "clientSecret" {
				t.Fatalf("unexpected field %s", f.Key)
			}
			if !strings.Contains(f.Help, "TODO(spec)") {
				t.Fatalf("%s.%s help must mark the unknown: %q", info.ID, f.Key, f.Help)
			}
		}
	}
}

func TestHandleRejectionFixes(t *testing.T) {
	for _, c := range []*hubjson.Connector{aetatmeen.New(), egeda.New()} {
		for _, code := range []string{"MOCK-BATCH-EXPIRY-MISSING", "MOCK-SERIAL-DUPLICATE", "MOCK-SERIAL-UNKNOWN", "MOCK-SERIAL-TOO-LONG",
			"MOCK-RECEIVER-GLN-MISSING", "MOCK-PARENT-MISSING", "MOCK-REPORT-TYPE-UNKNOWN", "OTHER"} {
			a := c.HandleRejection(context.Background(), connector.Rejection{Code: code, Message: "m", Payload: connector.Payload{Kind: "dispatch"}})
			if a.SuggestedFix == "" || !a.Retryable || !strings.Contains(a.Title, "DISPATCH") || !strings.Contains(a.Title, c.Profile.Hub) {
				t.Fatalf("%s %s: %+v", c.Info().ID, code, a)
			}
		}
		if a := c.HandleRejection(context.Background(), connector.Rejection{Code: "OTHER"}); a.SuggestedFix != c.Profile.DefaultFix {
			t.Fatalf("default fix: %+v", a)
		}
	}
}

func TestMap(t *testing.T) {
	c := aetatmeen.New()
	ctx := context.Background()
	if pl, _ := c.Map(ctx, connector.Event{BizStep: "inspecting", Packs: []connector.Pack{{GTIN: "G"}}}); pl != nil {
		t.Fatal("untriggered step must map to nothing")
	}
	if pl, _ := c.Map(ctx, connector.Event{BizStep: "shipping"}); pl != nil {
		t.Fatal("no packs, no containers: nothing to report")
	}
	pl, err := c.Map(ctx, connector.Event{EventID: "e", BizStep: "packing", Location: "6290000000004", Parent: "urn:epc:id:sscc:629000.0000000001",
		Containers: []string{"urn:b", "urn:a"}, EventTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("x", 3600)),
		Packs: []connector.Pack{{GTIN: "H", Serial: "1"}, {GTIN: "G", Serial: "2"}, {GTIN: "G", Serial: "1", Lot: "L", Expiry: "2030-01-01"}}})
	b := string(pl[0].Body)
	if err != nil || len(pl) != 1 || pl[0].Kind != "aggregation" || !strings.Contains(b, `"senderGln":"6290000000004"`) ||
		!strings.Contains(b, `"containers":["urn:a","urn:b"]`) || !strings.Contains(b, `"eventTime":"2025-12-31T23:00:00Z"`) ||
		strings.Index(b, `"gtin":"G","serial":"1"`) > strings.Index(b, `"gtin":"H"`) {
		t.Fatalf("map: %v %s", err, b)
	}
	// The key depends on the connector, so two hubs never share idempotency keys.
	pe, _ := egeda.New().Map(ctx, connector.Event{EventID: "e", BizStep: "packing", Packs: []connector.Pack{{GTIN: "G", Serial: "1"}}})
	if pe[0].Key == pl[0].Key {
		t.Fatal("keys must differ per connector")
	}
}

func TestSubmitAndPollErrors(t *testing.T) {
	var gotKey, gotUser, gotParty string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, _, _ = r.BasicAuth()
		gotParty = r.Header.Get("X-Participant-Id")
		if k := r.Header.Get("Idempotency-Key"); k != "" {
			gotKey = k
		}
		switch r.URL.Path {
		case "/ok/reports":
			_, _ = w.Write([]byte(`{"reportId":"R1"}`))
		case "/noid/reports":
			_, _ = w.Write([]byte(`{}`))
		case "/ok/reports/REJ":
			_, _ = w.Write([]byte(`{"status":"REJECTED","errorCode":"C","errorMessage":"m","failedItems":[{"gtin":"G","serial":"S"}]}`))
		case "/ok/reports/WAIT":
			_, _ = w.Write([]byte(`{"status":"PENDING"}`))
		case "/ok/reports/OK":
			_, _ = w.Write([]byte(`{"status":"ACCEPTED"}`))
		case "/ok/reports/BAD":
			_, _ = w.Write([]byte(`nope`))
		default:
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	c := egeda.New()
	ctx := context.Background()
	cfg := connector.Config{"baseUrl": srv.URL + "/ok/", "clientId": "cid", "clientSecret": "s", "participantId": "P1"}
	sub, err := c.Submit(ctx, cfg, connector.Payload{Key: "k1", Body: []byte(`{}`)})
	if err != nil || sub.Ref != "R1" || gotKey != "k1" || gotUser != "cid" || gotParty != "P1" {
		t.Fatalf("submit: %v %+v %s %s %s", err, sub, gotKey, gotUser, gotParty)
	}
	if _, err := c.Submit(ctx, connector.Config{"baseUrl": srv.URL + "/noid"}, connector.Payload{}); err == nil || !strings.Contains(err.Error(), "unexpected EDA response") {
		t.Fatalf("no id: %v", err)
	}
	if _, err := c.Submit(ctx, connector.Config{"baseUrl": srv.URL + "/down"}, connector.Payload{}); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("down: %v", err)
	}
	if _, err := c.Submit(ctx, connector.Config{"baseUrl": "http://[::1"}, connector.Payload{}); err == nil {
		t.Fatal("malformed url must fail")
	}
	if _, err := c.Submit(ctx, connector.Config{"baseUrl": "http://127.0.0.1:1"}, connector.Payload{}); err == nil {
		t.Fatal("unreachable must fail")
	}
	st, err := c.PollStatus(ctx, cfg, connector.Submission{Ref: "REJ"})
	if err != nil || st.State != connector.Rejected || st.Rejection.Code != "C" || len(st.Rejection.Packs) != 1 {
		t.Fatalf("rejected: %v %+v", err, st)
	}
	if st, _ := c.PollStatus(ctx, cfg, connector.Submission{Ref: "WAIT"}); st.State != connector.Pending {
		t.Fatalf("pending: %+v", st)
	}
	if st, _ := c.PollStatus(ctx, cfg, connector.Submission{Ref: "OK"}); st.State != connector.Accepted {
		t.Fatalf("accepted: %+v", st)
	}
	if _, err := c.PollStatus(ctx, cfg, connector.Submission{Ref: "BAD"}); err == nil {
		t.Fatal("non-JSON must fail")
	}
	if _, err := c.PollStatus(ctx, connector.Config{"baseUrl": srv.URL + "/down"}, connector.Submission{Ref: "x"}); err == nil {
		t.Fatal("5xx must fail")
	}
	if _, err := c.PollStatus(ctx, connector.Config{"baseUrl": "http://127.0.0.1:1"}, connector.Submission{Ref: "x"}); err == nil {
		t.Fatal("unreachable must fail")
	}
}
