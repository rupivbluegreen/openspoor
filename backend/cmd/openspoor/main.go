// Command openspoor is the single Open Spoor binary: api, worker, migrate, all, bootstrap and seed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openspoor/openspoor/internal/app"
	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/migrations"
)

const usage = `openspoor <command>

Commands:
  api        serve the HTTP API
  worker     run background jobs (projections, webhooks, connectors, agents)
  all        api + worker in one process (development, single-node installs)
  migrate    apply database migrations
  bootstrap  create the first tenant and admin (--slug --name --email --password)
  seed       load the demo data set (3 fictional companies, --packs N serialized packs)
  openapi    print the OpenAPI 3.1 document generated from the router
  version    print the version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd := os.Args[1]; cmd {
	case "version":
		fmt.Println(app.Version)
	case "openapi":
		a := mustApp(ctx, cfg)
		defer a.Close()
		b, err := a.OpenAPI()
		if err != nil {
			fatal(err)
		}
		_, _ = os.Stdout.Write(b)
	case "migrate":
		if err := migrations.Up(ctx, migrateURL(cfg)); err != nil {
			fatal(err)
		}
		v, _ := migrations.Status(ctx, migrateURL(cfg))
		slog.Info("migrations applied", "version", v)
	case "api", "worker", "all":
		a := mustApp(ctx, cfg)
		defer a.Close()
		if cmd != "api" {
			go a.Deps.Jobs.Run(ctx)
			go a.RunSchedulers(ctx)
		}
		if cmd == "worker" {
			slog.Info("worker started", "kinds", a.Deps.Jobs.Kinds())
			<-ctx.Done()
			return
		}
		serve(ctx, a, cfg)
	case "bootstrap":
		fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
		slug := fs.String("slug", "demo", "tenant slug")
		name := fs.String("name", "Demo Pharma", "tenant name")
		email := fs.String("email", "admin@example.com", "admin email")
		password := fs.String("password", "", "admin password (min 10 chars)")
		operator := fs.Bool("operator", false, "grant platform operator rights")
		_ = fs.Parse(os.Args[2:])
		a := mustApp(ctx, cfg)
		defer a.Close()
		t, u, err := a.Platform.CreateTenant(ctx, platform.NewTenant{Slug: *slug, Name: *name, AdminEmail: *email, AdminPassword: *password, Operator: *operator})
		if err != nil {
			fatal(err)
		}
		fmt.Printf("tenant %s (%s) created, admin %s\n", t.Slug, t.ID, u.Email)
	case "seed":
		fs := flag.NewFlagSet("seed", flag.ExitOnError)
		packs := fs.Int("packs", 10000, "serialized packs to create")
		password := fs.String("password", "openspoor-demo", "password for all demo users")
		_ = fs.Parse(os.Args[2:])
		a := mustApp(ctx, cfg)
		defer a.Close()
		if err := a.Seed(ctx, *packs, *password); err != nil {
			fatal(err)
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func migrateURL(cfg config.Config) string {
	if u := os.Getenv("OPENSPOOR_MIGRATE_DATABASE_URL"); u != "" {
		return u
	}
	return cfg.DatabaseURL
}

func mustApp(ctx context.Context, cfg config.Config) *app.App {
	a, err := app.New(ctx, cfg)
	if err != nil {
		fatal(err)
	}
	return a
}

func serve(ctx context.Context, a *app.App, cfg config.Config) {
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: a.Router, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("api listening", "addr", cfg.HTTPAddr, "mode", cfg.Mode, "version", app.Version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(err)
	}
}

func fatal(err error) {
	slog.Error("fatal", "err", err)
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
