package ecosystem

import (
	"strings"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/core/auth"
)

func temp(v float64) *float64 { return &v }

func TestDetectExcursions(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	// Unsorted input with a gap (nil temperature), a cold excursion and a warm one still open at the end.
	rs := []reading{
		{Time: at(50), TempC: temp(9)},
		{Time: at(0), TempC: temp(5)},
		{Time: at(10), TempC: temp(1.5)},
		{Time: at(20), TempC: temp(-1)},
		{Time: at(25), TempC: nil},
		{Time: at(30), TempC: temp(4)},
		{Time: at(40), TempC: temp(8.5)},
		{Time: at(60), TempC: temp(12)},
		{Time: at(70), TempC: temp(10)},
	}
	got := DetectExcursions(rs, 2, 8)
	if len(got) != 2 {
		t.Fatalf("excursions: %+v", got)
	}
	cold, warm := got[0], got[1]
	if !cold.StartedAt.Equal(at(10)) || !cold.EndedAt.Equal(at(30)) || cold.Minutes != 20 || cold.PeakC != -1 || cold.MinC != 2 || cold.MaxC != 8 {
		t.Fatalf("cold: %+v", cold)
	}
	if !warm.StartedAt.Equal(at(40)) || !warm.EndedAt.Equal(at(70)) || warm.Minutes != 30 || warm.PeakC != 12 {
		t.Fatalf("open-ended warm: %+v", warm)
	}
	if len(DetectExcursions([]reading{{Time: t0, TempC: temp(5)}, {Time: t0, TempC: nil}}, 2, 8)) != 0 {
		t.Fatal("in-range readings produced an excursion")
	}
}

func TestManifestValidate(t *testing.T) {
	ok := Manifest{ID: "acme-sync", Name: "Sync", Kind: "integration", Publisher: "Acme", Version: "1", Roles: []string{auth.RoleIntegration},
		Webhook: "https://hooks.example/x", Homepage: "https://acme.example"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(m *Manifest){
		"id with space":   func(m *Manifest) { m.ID = "a b" },
		"id with slash":   func(m *Manifest) { m.ID = "a/b" },
		"no name":         func(m *Manifest) { m.Name = "" },
		"no publisher":    func(m *Manifest) { m.Publisher = "" },
		"no version":      func(m *Manifest) { m.Version = "" },
		"bad kind":        func(m *Manifest) { m.Kind = "virus" },
		"admin role":      func(m *Manifest) { m.Roles = []string{auth.RoleAdmin} },
		"unknown role":    func(m *Manifest) { m.Roles = []string{"Overlord"} },
		"http webhook":    func(m *Manifest) { m.Webhook = "http://hooks.example" },
		"javascript link": func(m *Manifest) { m.Homepage = "javascript:alert(1)" },
		"http homepage":   func(m *Manifest) { m.Homepage = "http://acme.example" },
	}
	for name, mut := range cases {
		m := ok
		m.Roles = append([]string(nil), ok.Roles...)
		mut(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, k := range []string{"connector", "agent", "rulepack", "app"} {
		m := ok
		m.Kind = k
		if err := m.Validate(); err != nil {
			t.Errorf("kind %s rejected: %v", k, err)
		}
	}
}

func TestParseArticle(t *testing.T) {
	a := parseArticle("x.md", []byte("---\ntitle: Hello\ntags: [a, b ,c]\nother: z\n---\n\n# Body\n"))
	if a.Slug != "x" || a.Title != "Hello" || strings.Join(a.Tags, ",") != "a,b,c" || a.Body != "# Body" {
		t.Fatalf("front matter: %+v", a)
	}
	b := parseArticle("plain.md", []byte("  just text  "))
	if b.Title != "" || b.Tags != nil || b.Body != "just text" {
		t.Fatalf("plain: %+v", b)
	}
	// Unterminated front matter is kept as body.
	c := parseArticle("open.md", []byte("---\ntitle: X\nbody"))
	if c.Title != "" || !strings.HasPrefix(c.Body, "---") {
		t.Fatalf("unterminated: %+v", c)
	}
}

func TestPercentile(t *testing.T) {
	xs := []float64{80, 0, 75, 50, 66}
	if p := percentile(xs, 0.5); p != 66 {
		t.Fatalf("median %v", p)
	}
	if p := percentile(xs, 0.9); p < 77.9 || p > 78.1 {
		t.Fatalf("p90 %v", p)
	}
	if p := percentile([]float64{3}, 0.9); p != 3 {
		t.Fatalf("single %v", p)
	}
	if xs[0] != 80 {
		t.Fatal("percentile must not reorder its input")
	}
	if firstNonEmpty("", "", "x", "y") != "x" || firstNonEmpty() != "" {
		t.Fatal("firstNonEmpty")
	}
}
