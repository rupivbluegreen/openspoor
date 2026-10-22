// Package app wires every module into the single Open Spoor binary.
package app

import (
	"context"
	"embed"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/bus"
	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/jobs"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/core/secrets"
	"github.com/openspoor/openspoor/internal/platform"
)

// Version is set at build time with -ldflags.
var Version = "dev"

//go:embed openapi.yaml
var openapiFS embed.FS

// App is the assembled application.
type App struct {
	Deps     kernel.Deps
	Modules  []kernel.Module
	Platform *platform.Module
	Router   http.Handler
}

// New connects infrastructure and assembles modules.
func New(ctx context.Context, cfg config.Config) (*App, error) {
	d, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	b, err := bus.New(cfg.KafkaBrokers)
	if err != nil {
		d.Close()
		return nil, err
	}
	sealer, err := secrets.New(cfg.MasterKey)
	if err != nil {
		d.Close()
		return nil, err
	}
	deps := kernel.Deps{Cfg: cfg, DB: d, Bus: b, Jobs: jobs.NewRunner(d.Pool, cfg.WorkerConcurrency), Seal: sealer}
	plat := platform.New(deps)
	a, err := auth.New(ctx, cfg.JWTSecret, cfg.JWTTTL, plat, cfg.OIDCIssuer, cfg.OIDCAudience)
	if err != nil {
		d.Close()
		return nil, err
	}
	deps.Auth = a
	plat.Deps = deps
	app := &App{Deps: deps, Platform: plat}
	app.Modules, err = buildModules(deps, plat)
	if err != nil {
		d.Close()
		return nil, err
	}
	for _, m := range app.Modules {
		if w, ok := m.(kernel.Worker); ok {
			w.RegisterJobs(deps.Jobs)
		}
	}
	app.Router = app.routes()
	return app, nil
}

// Close releases resources.
func (a *App) Close() {
	a.Deps.Bus.Close()
	a.Deps.DB.Close()
}

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{Name: "openspoor_http_requests_total", Help: "HTTP requests"}, []string{"method", "route", "status"})
	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "openspoor_http_request_duration_seconds", Help: "HTTP latency",
		Buckets: []float64{.005, .01, .025, .05, .1, .2, .5, 1, 2, 5}}, []string{"method", "route"})
)

func metrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		httpRequests.WithLabelValues(r.Method, route, strconv.Itoa(ww.Status())).Inc()
		httpDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if r.URL.Path == "/healthz" || r.URL.Path == "/metrics" {
			return
		}
		slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "ms", time.Since(start).Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()))
	})
}

func (a *App) cors(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range a.Deps.Cfg.CORSOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && (allowed[o] || allowed["*"]) {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key, Idempotency-Key")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

func (a *App) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer, securityHeaders, logRequests, metrics, a.cors)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := a.Deps.DB.Pool.Ping(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
	r.Handle("/metrics", promhttp.Handler())
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/meta", func(w http.ResponseWriter, _ *http.Request) {
			names := make([]string, 0, len(a.Modules))
			for _, m := range a.Modules {
				names = append(names, m.Name())
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"name": "Open Spoor", "version": Version, "mode": a.Deps.Cfg.Mode, "modules": names,
				"devAuth": a.Deps.Cfg.DevAuth, "oidc": a.Deps.Cfg.OIDCIssuer != ""})
		})
		r.Get("/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
			b, _ := openapiFS.ReadFile("openapi.yaml")
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write(b)
		})
		for _, m := range a.Modules {
			if p, ok := m.(kernel.PublicRoutes); ok {
				p.Public(r)
			}
		}
		r.Group(func(r chi.Router) {
			r.Use(a.Deps.Auth.Middleware)
			for _, m := range a.Modules {
				m.Routes(r)
			}
		})
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			httpx.Fail(w, r, httpx.NotFound("no such endpoint"))
			return
		}
		http.NotFound(w, r)
	})
	return r
}
