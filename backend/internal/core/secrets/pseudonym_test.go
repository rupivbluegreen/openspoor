package secrets

import "testing"

func TestPseudonym(t *testing.T) {
	a, _ := New([]byte("tenant key A"))
	b, _ := New([]byte("tenant key B"))
	p := a.Pseudonym("Alice@Example.com")
	if len(p) != 16 || p != a.Pseudonym("  alice@example.com ") {
		t.Fatalf("not stable or not normalised: %q", p)
	}
	if p == a.Pseudonym("bob@example.com") {
		t.Fatal("different people share a token")
	}
	// Another tenant (or the same tenant after its key is destroyed and a new one made) cannot
	// reproduce the token, so erased people cannot be re-identified by guessing addresses.
	if p == b.Pseudonym("alice@example.com") {
		t.Fatal("tokens are not tied to the key")
	}
	if p == "" || p == "alice@example.com" {
		t.Fatal("token reveals the value")
	}
}
