package app

import (
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
)

// webApps maps URL prefixes to the single-page apps under OPENSPOOR_WEB_ROOT; the console owns "/".
var webApps = []struct{ prefix, dir string }{{"/verify/", "verify"}, {"/operator/", "operator"}, {"/", "console"}}

// webHandler serves the built frontends. Unknown paths inside an app fall back to its
// index.html so client-side routes survive a reload; hashed assets are cached for a year.
// os.DirFS with fs.ValidPath names cannot leave root.
func webHandler(root string) http.HandlerFunc {
	fsys := os.DirFS(root)
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
		switch r.URL.Path {
		case "/verify":
			http.Redirect(w, r, "/verify/", http.StatusMovedPermanently)
			return
		case "/operator":
			http.Redirect(w, r, "/operator/", http.StatusMovedPermanently)
			return
		}
		for _, a := range webApps {
			if !strings.HasPrefix(p+"/", a.prefix) {
				continue
			}
			name := path.Join(a.dir, strings.TrimPrefix(p, strings.TrimSuffix(a.prefix, "/")))
			if !fs.ValidPath(name) {
				http.NotFound(w, r)
				return
			}
			if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, a.dir+"/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				http.ServeFileFS(w, r, fsys, name) //nolint:gosec // G703: name passed fs.ValidPath inside os.DirFS(root)
				return
			}
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, fsys, a.dir+"/index.html")
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
