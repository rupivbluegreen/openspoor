package epcis_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/epcis"
	"github.com/openspoor/openspoor/internal/testkit"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

// fakeS3 records PUT Object requests (path-style: /<bucket>/<key>).
type fakeS3 struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
		http.Error(w, "unexpected", http.StatusBadRequest)
		return
	}
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.objs[r.URL.Path] = b
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func TestArchiveOldCaptures(t *testing.T) {
	s3 := &fakeS3{objs: map[string][]byte{}}
	srv := httptest.NewServer(s3)
	defer srv.Close()
	e := testkit.New(t, func(c *config.Config) {
		c.S3Endpoint, c.S3Bucket, c.S3AccessKey, c.S3SecretKey = srv.URL, "archive", "key", "secret"
		c.ArchiveAfterDays = 0 // archive immediately
	})
	var mod *epcis.Module
	for _, m := range e.App.Modules {
		if x, ok := m.(*epcis.Module); ok {
			mod = x
		}
	}
	poolID := setupProduct(t, e)
	serials := allocate(t, e, poolID, 2, "arch-1")
	comm := ev(lib.ObjectEvent, "ADD", "commissioning", "active", time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC))
	comm.EPCList = []string{urn(serials[0]), urn(serials[1])}
	var c epcis.Capture
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", doc(comm)), &c)
	e.Drain() // projection moves the capture from accepted to completed, as in production

	if err := mod.ArchiveOld(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s3.objs) != 1 {
		t.Fatalf("archived %d objects, want 1", len(s3.objs))
	}
	for path, body := range s3.objs {
		if !strings.HasPrefix(path, "/archive/"+e.Tenant.ID.String()+"/epcis/") || !strings.HasSuffix(path, c.ID.String()+".jsonld.gz") {
			t.Fatalf("unexpected key %s", path)
		}
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(zr)
		d, err := lib.ParseJSON(raw)
		if err != nil {
			t.Fatal("archived document does not parse:", err)
		}
		if len(d.Body.EventList) != 1 {
			t.Fatalf("archived document has %d events, want 1", len(d.Body.EventList))
		}
	}
	// A second run finds nothing left to archive.
	if err := mod.ArchiveOld(t.Context()); err != nil || len(s3.objs) != 1 {
		t.Fatalf("second run: err=%v objects=%d", err, len(s3.objs))
	}
	var audit struct{ Items []map[string]any }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=epcis.archive", nil), &audit)
	if len(audit.Items) != 1 {
		t.Fatalf("archive audit entries: %d", len(audit.Items))
	}
}
