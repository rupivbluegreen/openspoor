package appsdk

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sign(secret string, b []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(b)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestWebhook(t *testing.T) {
	body := []byte(`{"type":"EPCISQueryDocument"}`)
	got := 0
	h := WebhookHandler("s3cret", func(_ context.Context, doc json.RawMessage) error { got++; return nil })
	for _, c := range []struct {
		sig  string
		want int
	}{{sign("s3cret", body), 204}, {sign("other", body), 401}, {"", 401}} {
		req := httptest.NewRequest("POST", "/hook", strings.NewReader(string(body)))
		req.Header.Set("X-OpenSpoor-Signature", c.sig)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Fatalf("sig %q: %d", c.sig, rec.Code)
		}
	}
	if got != 1 {
		t.Fatal("handler not called exactly once")
	}
}

func TestClientErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "k" || r.Header.Get("Idempotency-Key") != "i" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"title":"pool exhausted","status":409}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "k").Allocate(context.Background(), "p", 10, "i")
	var ae *APIError
	if !asErr(err, &ae) || ae.Status != 409 || ae.Title != "pool exhausted" {
		t.Fatalf("error mapping: %v", err)
	}
}

func asErr(err error, target **APIError) bool {
	e, ok := err.(*APIError)
	if ok {
		*target = e
	}
	return ok
}
