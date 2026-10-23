package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// tok stands in for the keyed hash: stable, lower-cased, and not containing the value.
func tok(v string) string {
	h := sha256.Sum256([]byte(strings.ToLower(v)))
	return hex.EncodeToString(h[:4])
}

func fake(calls *int) Pseudonymizer {
	return func(_ context.Context, _ uuid.UUID, v string) (string, error) {
		*calls++
		return tok(v), nil
	}
}

func TestScrub(t *testing.T) {
	var calls int
	SetPseudonymizer(fake(&calls))
	tenant := uuid.New()
	ctx := context.Background()

	// Personal keys are replaced at any depth; other fields are untouched.
	raw := json.RawMessage(`{"email":"Alice@Example.com","roles":["Admin"],"contacts":[{"phone":"+31 6 1234","site":"Amsterdam"}],"name":"Alice","signer":"bob@example.com"}`)
	out, err := scrub(ctx, tenant, Record{Entity: "user"}, raw)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["email"] != Token+tok("alice@example.com") || got["name"] != Token+tok("Alice") || got["signer"] != Token+tok("bob@example.com") {
		t.Fatalf("top level: %s", out)
	}
	if c := got["contacts"].([]any)[0].(map[string]any); c["phone"] != Token+tok("+31 6 1234") || c["site"] != "Amsterdam" {
		t.Fatalf("nested: %s", out)
	}
	if strings.Contains(string(out), "@example.com") || got["roles"].([]any)[0] != "Admin" {
		t.Fatalf("leak or damage: %s", out)
	}

	// A person-name key only counts for entities that hold people: a product name stays readable.
	calls = 0
	out, _ = scrub(ctx, tenant, Record{Entity: "product"}, json.RawMessage(`{"name":"Amoxicillin 500mg","gtin":"09506000134352"}`))
	if string(out) != `{"name":"Amoxicillin 500mg","gtin":"09506000134352"}` || calls != 0 {
		t.Fatalf("product snapshot changed: %s (calls %d)", out, calls)
	}
	// Nothing personal: the keyring is never touched.
	out, _ = scrub(ctx, tenant, Record{Entity: "serial"}, json.RawMessage(`{"status":"active"}`))
	if calls != 0 || string(out) != `{"status":"active"}` {
		t.Fatalf("keyring used without personal data: %d", calls)
	}
	// Already tokenised values are not tokenised again.
	out, _ = scrub(ctx, tenant, Record{Entity: "user"}, json.RawMessage(`{"email":"pseudo:abc"}`))
	if calls != 0 || string(out) != `{"email":"pseudo:abc"}` {
		t.Fatalf("double tokenising: %s", out)
	}
}

func TestScrubActor(t *testing.T) {
	var calls int
	SetPseudonymizer(fake(&calls))
	ctx, tenant := context.Background(), uuid.New()
	for in, want := range map[string]string{
		"user:alice@example.com":       "user:pseudo:" + tok("alice@example.com"),
		"operator:ops@example.com":     "operator:pseudo:" + tok("ops@example.com"),
		"user:01a166db-9672-7b71-a177": "user:01a166db-9672-7b71-a177",
		"system":                       "system",
		"apikey:abc":                   "apikey:abc",
	} {
		got, err := scrubActor(ctx, tenant, in)
		if err != nil || got != want {
			t.Errorf("scrubActor(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestNoPseudonymizerNeverWritesClearValues(t *testing.T) {
	pseudonymizer.Store(nil)
	out, err := scrub(context.Background(), uuid.New(), Record{Entity: "user"}, json.RawMessage(`{"email":"a@b.example"}`))
	if err != nil || strings.Contains(string(out), "a@b.example") {
		t.Fatalf("clear value written without a keyring: %s %v", out, err)
	}
}
