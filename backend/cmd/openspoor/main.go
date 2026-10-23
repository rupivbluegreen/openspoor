// Command openspoor is the single Open Spoor binary: api, worker, migrate, all, bootstrap and seed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openspoor/openspoor/internal/app"
	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/core/telemetry"
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
  rotate-keys re-seal the data keys and node key under OPENSPOOR_NEW_MASTER_KEY (--dry-run to check only)
  eval-agents run the agent golden datasets and print the score table (--tenant SLUG records the scores, --agent NAME limits it)
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
		shutdown, err := telemetry.Setup(ctx, "openspoor", app.Version)
		if err != nil {
			fatal(err)
		}
		defer func() { _ = shutdown(context.WithoutCancel(ctx)) }()
		if os.Getenv("OPENSPOOR_MIGRATE_ON_START") == "true" {
			if err := migrations.Up(ctx, migrateURL(cfg)); err != nil {
				fatal(err)
			}
		}
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
	case "rotate-keys":
		fs := flag.NewFlagSet("rotate-keys", flag.ExitOnError)
		dry := fs.Bool("dry-run", false, "open every sealed key with the current master key and the replacement, change nothing")
		_ = fs.Parse(os.Args[2:])
		a := mustApp(ctx, cfg)
		defer a.Close()
		if err := runRotateKeys(ctx, a, cfg, *dry, os.Stdout); err != nil {
			fatal(err)
		}
	case "eval-agents":
		fs := flag.NewFlagSet("eval-agents", flag.ExitOnError)
		tenant := fs.String("tenant", "", "record the scores for this tenant (slug)")
		var only multiFlag
		fs.Var(&only, "agent", "evaluate only this agent (repeatable)")
		_ = fs.Parse(os.Args[2:])
		a := mustApp(ctx, cfg)
		defer a.Close()
		res, err := a.EvalAgents(ctx, os.Stdout, *tenant, only)
		if err != nil {
			fatal(err)
		}
		for _, r := range res {
			if r.Error != "" || !r.Pass {
				os.Exit(1)
			}
		}
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

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return fmt.Sprint(*m) }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// runRotateKeys re-seals the master-key-sealed material under cfg.NewMasterKey and reports it on out.
func runRotateKeys(ctx context.Context, a *app.App, cfg config.Config, dryRun bool, out io.Writer) error {
	if len(cfg.NewMasterKey) == 0 {
		return errors.New("set OPENSPOOR_NEW_MASTER_KEY (plain, file:/path or vault: reference) to the replacement master key")
	}
	rep, err := a.RotateKeys(ctx, cfg.NewMasterKey, dryRun)
	if err != nil {
		return err
	}
	verb := "re-sealed"
	if rep.DryRun {
		verb = "would re-seal (dry run, nothing changed)"
	}
	fmt.Fprintf(out, "%s: %d tenant data keys, %d federation node key; %d clear-text webhook secrets sealed\n", verb, rep.TenantKeys, rep.NodeKeys, rep.WebhookSecretsSealed)
	if !rep.DryRun {
		fmt.Fprintln(out, "Next: set OPENSPOOR_MASTER_KEY to the new key, unset OPENSPOOR_NEW_MASTER_KEY and restart every api and worker process.")
	}
	return nil
}

func migrateURL(cfg config.Config) string {
	if cfg.MigrateDatabaseURL != "" {
		return cfg.MigrateDatabaseURL
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
	srv, err := newServer(a.Router, cfg)
	if err != nil {
		fatal(err)
	}
	servers := []*http.Server{srv}
	if cfg.TLS.RedirectAddr != "" {
		servers = append(servers, &http.Server{Addr: cfg.TLS.RedirectAddr, Handler: config.RedirectHandler(cfg.PublicURL), ReadHeaderTimeout: 10 * time.Second})
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		for _, s := range servers {
			_ = s.Shutdown(sctx)
		}
	}()
	if len(servers) > 1 {
		go func() {
			if err := servers[1].ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fatal(err)
			}
		}()
	}
	tlsOn := srv.TLSConfig != nil
	attrs := []any{"addr", cfg.HTTPAddr, "mode", cfg.Mode, "version", app.Version, "tls", tlsOn}
	if tlsOn {
		attrs = append(attrs, "tls_min_version", config.VersionName(srv.TLSConfig.MinVersion), "federation_client_cert_required", cfg.TLS.RequireFederationClientCert)
	}
	slog.Info("api listening", attrs...)
	if tlsOn {
		// The certificate comes from TLSConfig.GetCertificate (reloaded when the files change).
		err = srv.ListenAndServeTLS("", "")
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(err)
	}
}

// newServer builds the API server; with OPENSPOOR_TLS_CERT/KEY it serves HTTPS itself.
func newServer(h http.Handler, cfg config.Config) (*http.Server, error) {
	tc, err := cfg.ServerTLS()
	if err != nil {
		return nil, err
	}
	return &http.Server{Addr: cfg.HTTPAddr, Handler: h, TLSConfig: tc, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}, nil
}

func fatal(err error) {
	slog.Error("fatal", "err", err)
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
