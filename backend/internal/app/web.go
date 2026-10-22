package app

import (
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// webApps maps URL prefixes to the single-page apps under OPENSPOOR_WEB_ROOT; the console owns "/".
var webApps = []struct{ prefix, dir string }{{"/verify/", "verify"}, {"/operator/", "operator"}, {"/", "console"}}

// webHandler serves the built frontends. Unknown paths inside an app fall back to its
// index.html so client-side routes survive a reload; hashed assets are cached for a year.
func webHandler(root string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		// A brand-hosted GS1 Digital Link lands on the public verify page.
		if strings.HasPrefix(p, "/01/") {
			http.Redirect(w, r, "/verify/?code="+url.QueryEscape(publicURL(r)), http.StatusFound)
			return
		}
		if r.URL.Path == "/verify" || r.URL.Path == "/operator" {
			http.Redirect(w, r, p+"/", http.StatusMovedPermanently)
			return
		}
		for _, a := range webApps {
			if !strings.HasPrefix(p+"/", a.prefix) {
				continue
			}
			dir := filepath.Join(root, a.dir)
			rel := strings.TrimPrefix(p, strings.TrimSuffix(a.prefix, "/"))
			file := filepath.Join(dir, filepath.FromSlash(rel))
			if st, err := os.Stat(file); err == nil && !st.IsDir() {
				if strings.Contains(rel, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				http.ServeFile(w, r, file)
				return
			}
			if path.Ext(rel) != "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		http.NotFound(w, r)
	}
}

func publicURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + r.URL.RequestURI()
}
