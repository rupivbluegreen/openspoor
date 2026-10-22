package serialization

import (
	"crypto/rand"
	"testing"
)

func newGen(t testing.TB, mode string) *Generator {
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	g, err := NewGenerator(mode, "", 12, "", key, []byte("01095060001343520"))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestRandomSerialsUniqueAndReversible(t *testing.T) {
	g := newGen(t, "random")
	const n = 200_000
	serials, err := g.Range(0, n)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]struct{}, n)
	for i, s := range serials {
		if len(s) != 12 {
			t.Fatalf("serial %q has wrong length", s)
		}
		if _, dup := seen[s]; dup {
			t.Fatalf("duplicate serial %s at %d", s, i)
		}
		seen[s] = struct{}{}
		if i%997 == 0 {
			c, err := g.Counter(s)
			if err != nil || c != int64(i) {
				t.Fatalf("counter(%s) = %d, %v; want %d", s, c, err, i)
			}
		}
	}
	// Consecutive counters must not give consecutive-looking serials.
	if serials[0][:6] == serials[1][:6] && serials[1][:6] == serials[2][:6] {
		t.Fatalf("serials look sequential: %v", serials[:3])
	}
}

func TestSequentialAndPrefix(t *testing.T) {
	g, err := NewGenerator("sequential", "0123456789", 8, "SA", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := g.Serial(42)
	if s != "SA00000042" {
		t.Fatalf("got %s", s)
	}
	if c, _ := g.Counter(s); c != 42 {
		t.Fatalf("counter %d", c)
	}
	if _, err := g.Counter("XX00000042"); err == nil {
		t.Fatal("foreign prefix accepted")
	}
}

func TestDomainMinimum(t *testing.T) {
	if _, err := NewGenerator("random", "0123456789", 5, "", make([]byte, 16), nil); err == nil {
		t.Fatal("10^5 domain must be rejected for FF1")
	}
}

// BenchmarkAllocate100k measures generating 100,000 random serials (all cores).
func BenchmarkAllocate100k(b *testing.B) {
	g := newGen(b, "random")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := g.Range(int64(i)*100_000, 100_000); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(100_000*b.N)/b.Elapsed().Seconds(), "serials/s")
}

func TestLowWatermarkNoOverflow(t *testing.T) {
	const capacity = int64(1) << 62
	if lowWatermark(capacity-1, capacity, 10) {
		t.Fatal("full pool reported low")
	}
	if !lowWatermark(capacity/20, capacity, 10) {
		t.Fatal("5% remaining not reported low at 10% watermark")
	}
}
