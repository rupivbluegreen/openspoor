package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebHandler(t *testing.T) {
	root := t.TempDir()
	for _, app := range []string{"console", "verify", "operator"} {
		if err := os.MkdirAll(filepath.Join(root, app, "assets"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(root, app, "index.html"), []byte("index-"+app), 0o644)
		_ = os.WriteFile(filepath.Join(root, app, "assets", "a.js"), []byte("js-"+app), 0o644)
	}
	_ = os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret"), 0o644)
	h := webHandler(root)
	cases := []struct {
		path, wantBody string
		wantCode       int
	}{
		{"/", "index-console", 200},
		{"/shipments/123", "index-console", 200},
		{"/assets/a.js", "js-console", 200},
		{"/verify/", "index-verify", 200},
		{"/verify/playground", "index-verify", 200},
		{"/verify/assets/a.js", "js-verify", 200},
		{"/operator/tenants", "index-operator", 200},
		{"/assets/missing.js", "", 404},
		{"/../secret.txt", "", 404},
		{"/verify/../../secret.txt", "", 404},
		{"/verify", "", 301},
		{"/01/09506000134352/21/ABC", "", 302},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.URL.Path = c.path
		h(rec, req)
		if rec.Code != c.wantCode || (c.wantBody != "" && !strings.Contains(rec.Body.String(), c.wantBody)) {
			t.Errorf("%s: got %d %q, want %d %q", c.path, rec.Code, rec.Body.String(), c.wantCode, c.wantBody)
		}
	}
}
