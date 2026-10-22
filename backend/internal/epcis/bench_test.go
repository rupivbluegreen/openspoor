package epcis_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

// TestBench100k measures end-to-end capture of a 100,000-event EPCIS document through
// the HTTP API against real PostgreSQL, plus asynchronous projection, serial allocation
// and 1k-event capture latency. It only runs with OPENSPOOR_BENCH=1 and writes
// bench-results/capture.json at the repo root.
func TestBench100k(t *testing.T) {
	if os.Getenv("OPENSPOOR_BENCH") != "1" {
		t.Skip("set OPENSPOOR_BENCH=1")
	}
	n, _ := strconv.Atoi(os.Getenv("OPENSPOOR_BENCH_EVENTS"))
	if n == 0 {
		n = 100_000
	}
	e := testkit.New(t)
	poolID := setupProduct(t, e)

	// 1. Allocation throughput through the API (one request of n serials).
	t0 := time.Now()
	serials := allocate(t, e, poolID, n+1000*25, "bench")
	allocDur := time.Since(t0)

	// 2. Build an n-event document: each pack commissioned in its own ObjectEvent.
	base := time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)
	events := make([]lib.Event, n)
	for i := 0; i < n; i++ {
		ev := ev(lib.ObjectEvent, "ADD", "commissioning", "active", base.Add(time.Duration(i)*time.Millisecond))
		ev.EPCList = []string{urn(serials[i])}
		ev.ILMD = map[string]any{"lotNumber": "B1", "itemExpirationDate": "2028-12-31"}
		events[i] = ev
	}
	body := doc(events...)

	t0 = time.Now()
	r := e.Do("POST", "/api/v1/epcis/capture", body)
	captureDur := time.Since(t0)
	if r.Status != http.StatusAccepted {
		t.Fatalf("capture: %d %.300s", r.Status, r.Body)
	}
	var c struct {
		EventCount int
		Timing     map[string]float64 `json:"timingMs"`
	}
	_ = r.JSON(&c)
	if c.EventCount != n {
		t.Fatalf("stored %d of %d", c.EventCount, n)
	}

	// 3. Projection: serial state queryable.
	t0 = time.Now()
	e.Drain()
	projDur := time.Since(t0)
	var stats struct{ Serials int }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/stats", nil), &stats)
	if stats.Serials != n {
		t.Fatalf("projected %d of %d", stats.Serials, n)
	}

	// 4. Capture ack latency for 1k-event documents (25 sequential runs).
	var lat []float64
	for run := 0; run < 25; run++ {
		evs := make([]lib.Event, 1000)
		for i := range evs {
			x := ev(lib.ObjectEvent, "ADD", "commissioning", "active", base.Add(time.Hour+time.Duration(run*1000+i)*time.Millisecond))
			x.EPCList = []string{urn(serials[n+run*1000+i])}
			evs[i] = x
		}
		b := doc(evs...)
		s := time.Now()
		rr := e.Do("POST", "/api/v1/epcis/capture", b)
		lat = append(lat, float64(time.Since(s).Microseconds())/1000)
		if rr.Status != http.StatusAccepted {
			t.Fatalf("1k capture %d: %s", rr.Status, rr.Body)
		}
	}
	sort.Float64s(lat)
	p := func(q float64) float64 { return lat[int(q*float64(len(lat)-1))] }

	// 5. Direct (in-process) capture of the same size, without HTTP transfer.
	_ = auth.Principal{}
	res := map[string]any{
		"measuredAt":          time.Now().UTC().Format(time.RFC3339),
		"cpus":                runtime.NumCPU(),
		"goVersion":           runtime.Version(),
		"events":              n,
		"documentBytes":       len(body),
		"captureSeconds":      captureDur.Seconds(),
		"captureEventsPerSec": float64(n) / captureDur.Seconds(),
		"captureTimingMs":     c.Timing,
		"projectionSeconds":   projDur.Seconds(),
		"projectionPerSec":    float64(n) / projDur.Seconds(),
		"allocateSerials":     n + 25000,
		"allocateSeconds":     allocDur.Seconds(),
		"allocatePerSec":      float64(n+25000) / allocDur.Seconds(),
		"capture1kP50Ms":      p(0.5),
		"capture1kP95Ms":      p(0.95),
		"capture1kMaxMs":      lat[len(lat)-1],
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	t.Logf("%s", out)
	dir := filepath.Join("..", "..", "..", "bench-results")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("capture-%d.json", n)), out, 0o644)
	_ = context.Background()
}
