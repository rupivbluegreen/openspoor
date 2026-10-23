package serialization

import (
	"strings"
	"testing"
)

var key16 = []byte("0123456789abcdef")

func TestNewGeneratorValidation(t *testing.T) {
	cases := []struct {
		name, mode, alphabet string
		length               int
		prefix               string
		key                  []byte
		want                 string
	}{
		{"one-char alphabet", "sequential", "A", 6, "", nil, "2-62"},
		{"non-ascii alphabet", "sequential", "01é", 6, "", nil, "distinct ASCII"},
		{"repeated char", "sequential", "0012", 6, "", nil, "distinct ASCII"},
		{"small random domain", "random", "0123456789", 5, "", key16, "1,000,000"},
		{"bad key", "random", "", 12, "", []byte("short"), "16, 24 or 32"},
		{"unknown mode", "zigzag", "", 12, "", key16, "random or sequential"},
		{"too long", "sequential", "", 15, "ABCDEF", nil, "≤ 20"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewGenerator(tc.mode, tc.alphabet, tc.length, tc.prefix, tc.key, []byte("tweak"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if _, err := NewGenerator("random", "", 12, "", make([]byte, 32), []byte("t")); err != nil {
		t.Fatalf("AES-256 key: %v", err)
	}
}

func TestSequentialGenerator(t *testing.T) {
	g, err := NewGenerator("sequential", "0123456789ABCDEF", 4, "P-", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if g.Capacity() != 65536 {
		t.Fatalf("capacity %d", g.Capacity())
	}
	s, _ := g.Serial(255)
	if s != "P-00FF" {
		t.Fatalf("serial(255) = %s", s)
	}
	rs, err := g.Range(65534, 2)
	if err != nil || strings.Join(rs, ",") != "P-FFFE,P-FFFF" {
		t.Fatalf("range: %v %v", rs, err)
	}
	if n, err := g.Counter("P-0100"); err != nil || n != 256 {
		t.Fatalf("counter: %d %v", n, err)
	}
	for _, bad := range []string{"0100", "P-100", "P-01001", "P-01G0"} {
		if _, err := g.Counter(bad); err == nil {
			t.Errorf("Counter(%q) accepted", bad)
		}
	}
	// Huge domains are capped so int64 arithmetic stays safe.
	big, err := NewGenerator("sequential", DefaultAlphabet, 20, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if big.Capacity() != int64(1)<<62 {
		t.Fatalf("capped capacity: %d", big.Capacity())
	}
	if _, err := big.Counter(strings.Repeat("Z", 20)); err == nil {
		t.Fatal("counter beyond int64 accepted")
	}
}

func TestRandomRangeParallelMatchesSerial(t *testing.T) {
	g, err := NewGenerator("random", "", 10, "R", key16, []byte("gtinEU"))
	if err != nil {
		t.Fatal(err)
	}
	rs, err := g.Range(1000, 5000)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 1234, 4999} {
		s, _ := g.Serial(int64(1000 + i))
		if rs[i] != s || !strings.HasPrefix(s, "R") || len(s) != 11 {
			t.Fatalf("range[%d]=%s serial=%s", i, rs[i], s)
		}
		if n, err := g.Counter(s); err != nil || n != int64(1000+i) {
			t.Fatalf("counter(%s) = %d %v", s, n, err)
		}
	}
	// A different tweak (another GTIN/market) yields different serials from the same key.
	g2, _ := NewGenerator("random", "", 10, "R", key16, []byte("gtinUS"))
	s2, _ := g2.Serial(1000)
	if s2 == rs[0] {
		t.Fatal("tweak does not separate pools")
	}
}
