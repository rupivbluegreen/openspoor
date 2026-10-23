package ecosystem

import (
	"context"
	"math"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func approx(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > 1e-6 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func day(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestLibraryDefinitionsAreConsistent(t *testing.T) {
	idRE := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	cats := map[string]bool{}
	for _, c := range KPICategories {
		cats[c.Key] = true
	}
	seen := map[string]bool{}
	perCat := map[string]int{}
	for _, k := range Library {
		t.Run(k.ID, func(t *testing.T) {
			if !idRE.MatchString(k.ID) || seen[k.ID] {
				t.Errorf("id %q malformed or duplicate", k.ID)
			}
			seen[k.ID] = true
			if !cats[k.Category] {
				t.Errorf("unknown category %q", k.Category)
			}
			perCat[k.Category]++
			if k.Name == "" || k.Description == "" || k.Unit == "" {
				t.Error("name, description and unit are required")
			}
			if k.Direction != HigherBetter && k.Direction != LowerBetter {
				t.Errorf("direction %q", k.Direction)
			}
			if strings.TrimSpace(k.SQL) == "" {
				t.Fatal("no SQL")
			}
			switch k.Kind {
			case KindRatio:
				if k.Unit == "%" && k.Scale != 100 {
					t.Error("a percentage ratio is scaled by 100")
				}
				if k.Unit != "%" && k.Scale > 1 {
					t.Error("only percentages are scaled")
				}
			case KindQuantile:
				if k.Q <= 0 || k.Q >= 1 {
					t.Errorf("quantile %v", k.Q)
				}
			case KindSum:
			case KindSnapshot:
				if strings.Contains(k.SQL, "$1") {
					t.Error("a snapshot takes no window")
				}
				if k.Peer {
					t.Error("snapshots have no peer comparison")
				}
			default:
				t.Errorf("kind %q", k.Kind)
			}
			if k.Kind != KindSnapshot && (k.SeriesSQL != "" || k.BucketSQL != "") {
				t.Error("series and bucket SQL are for snapshots")
			}
			if k.Kind != KindSnapshot && (!strings.Contains(k.SQL, "$1") || !strings.Contains(k.SQL, "$2")) {
				t.Error("an event query is bounded by $1 and $2")
			}
			if k.Peer && k.Kind == KindSum {
				t.Error("volumes are not compared with peers")
			}
			if k.Kind != KindSnapshot && !strings.Contains(k.SQL, "tenant_id") {
				t.Error("event rows carry tenant_id")
			}
		})
	}
	for _, c := range KPICategories {
		if perCat[c.Key] == 0 {
			t.Errorf("category %s is empty", c.Key)
		}
	}
	if len(Library) < 30 {
		t.Errorf("library has %d KPIs", len(Library))
	}
	if _, ok := KPIByID("otif_pct"); !ok {
		t.Error("KPIByID")
	}
	if _, ok := KPIByID("nope"); ok {
		t.Error("KPIByID unknown")
	}
}

func TestAggregateKinds(t *testing.T) {
	rows := []row{{Day: day("2026-07-01"), A: 1, B: 1}, {Day: day("2026-07-01"), A: 0, B: 1}, {Day: day("2026-07-02"), A: 1, B: 1}, {Day: day("2026-07-02"), A: 1, B: 1}}
	pct := KPI{Kind: KindRatio, Scale: 100}
	v, n := pct.aggregate(rows)
	approx(t, v, 75)
	if n != 4 {
		t.Errorf("samples %d", n)
	}
	mean := KPI{Kind: KindRatio}
	v, _ = mean.aggregate([]row{{A: 6, B: 1}, {A: 12, B: 1}})
	approx(t, v, 9)
	if v, n := pct.aggregate(nil); v != nil || n != 0 {
		t.Error("a ratio without denominator has no value")
	}
	sum := KPI{Kind: KindSum}
	v, n = sum.aggregate([]row{{A: 2}, {A: 3}})
	approx(t, v, 5)
	if n != 2 {
		t.Errorf("sum rows %d", n)
	}
	v, _ = sum.aggregate(nil)
	approx(t, v, 0)
	q := KPI{Kind: KindQuantile, Q: 0.95}
	v, n = q.aggregate([]row{{A: 10}, {A: 20}, {A: 30}, {A: 40}, {A: 100}})
	approx(t, v, 88)
	if n != 5 {
		t.Errorf("samples %d", n)
	}
	med := KPI{Kind: KindQuantile, Q: 0.5}
	v, _ = med.aggregate([]row{{A: 2}, {A: 6}})
	approx(t, v, 4)
	if v, _ := med.aggregate(nil); v != nil {
		t.Error("no samples, no quantile")
	}
	if v, n := (KPI{Kind: KindSnapshot}).aggregate(rows); v != nil || n != 0 {
		t.Error("snapshots do not aggregate rows")
	}
	if (KPI{}).scale() != 1 || pct.scale() != 100 {
		t.Error("scale")
	}
}

func TestWindowAndSeries(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 30, 0, 0, time.UTC)
	w := NewWindow(now, 3)
	if !w.From.Equal(day("2026-07-08")) || w.Days != 3 {
		t.Fatalf("window %+v", w)
	}
	days := w.days()
	if len(days) != 3 || days[0].Format("2006-01-02") != "2026-07-08" || days[2].Format("2006-01-02") != "2026-07-10" {
		t.Fatalf("days %v", days)
	}
	rows := []row{{Day: day("2026-07-08"), A: 1, B: 1}, {Day: day("2026-07-08"), A: 0, B: 1}, {Day: day("2026-07-10"), A: 1, B: 1}}
	pts := (KPI{Kind: KindRatio, Scale: 100}).series(rows, w)
	if len(pts) != 3 || pts[1].Value != nil {
		t.Fatalf("a day without data is null: %+v", pts)
	}
	approx(t, pts[0].Value, 50)
	approx(t, pts[2].Value, 100)
	sp := (KPI{Kind: KindSum}).series([]row{{Day: day("2026-07-09"), A: 4}}, w)
	approx(t, sp[0].Value, 0)
	approx(t, sp[1].Value, 4)
	if sp[0].Day != "2026-07-08" {
		t.Errorf("point day %s", sp[0].Day)
	}
	// A window of one day is today only.
	if one := NewWindow(now, 1); len(one.days()) != 1 || one.From.Hour() != 0 {
		t.Errorf("one-day window %+v", one)
	}
}

func TestPeerValuesAndGoalStatus(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	rows := []row{{Tenant: a, A: 1, B: 1}, {Tenant: a, A: 0, B: 1}, {Tenant: b, A: 1, B: 1}, {Tenant: uuid.New(), A: 0, B: 0}}
	got := (KPI{Kind: KindRatio, Scale: 100}).peerValues(rows)
	if len(got) != 2 || got[a] != 50 || got[b] != 100 {
		t.Fatalf("peer values %v", got)
	}
	hi, lo := 90.0, 10.0
	g := &goal{Target: 80}
	for _, c := range []struct {
		dir  string
		v    *float64
		g    *goal
		want string
	}{{HigherBetter, &hi, g, "met"}, {HigherBetter, &lo, g, "missed"}, {LowerBetter, &lo, g, "met"}, {LowerBetter, &hi, g, "missed"}, {LowerBetter, nil, g, ""}, {LowerBetter, &lo, nil, ""}} {
		if got := goalStatus(c.dir, c.v, c.g); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.dir, c.v, got, c.want)
		}
	}
	if _, err := libraryPeerValues(context.Background(), nil, KPI{Peer: false}, Window{}); err == nil {
		t.Error("a KPI without peer comparison must refuse")
	}
}

func TestParseDays(t *testing.T) {
	for in, want := range map[string]int{"": 30, "7": 7, "365": 365} {
		r := httptest.NewRequest("GET", "/kpis?days="+in, nil)
		if got, err := parseDays(r); err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"0", "366", "x", "-1"} {
		if _, err := parseDays(httptest.NewRequest("GET", "/kpis?days="+in, nil)); err == nil {
			t.Errorf("%q must be rejected", in)
		}
	}
}
