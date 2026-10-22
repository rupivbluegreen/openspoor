// Package kernel holds the shared dependencies every module receives.
package kernel

import (
	"context"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/bus"
	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/jobs"
)

// Deps are injected into every module.
type Deps struct {
	Cfg  config.Config
	DB   *db.DB
	Bus  *bus.Bus
	Jobs *jobs.Runner
	Auth *auth.Authenticator
}

// Module is implemented by each bounded context.
type Module interface {
	Name() string
	// Routes mounts authenticated tenant routes under /api/v1.
	Routes(r chi.Router)
}

// PublicRoutes is implemented by modules exposing unauthenticated endpoints (login, VRS, verify, playground).
type PublicRoutes interface {
	Public(r chi.Router)
}

// Worker is implemented by modules with background jobs or bus subscriptions.
type Worker interface {
	RegisterJobs(r *jobs.Runner)
}

// Tx runs fn in a transaction scoped to the caller's tenant.
func (d Deps) Tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return d.DB.Tx(ctx, auth.MustFrom(ctx).TenantID, fn)
}

// TenantOf returns the caller's tenant.
func TenantOf(ctx context.Context) uuid.UUID { return auth.MustFrom(ctx).TenantID }

// Schedule is a periodic system task (runs in the worker process).
type Schedule struct {
	Name  string
	Every time.Duration
	Run   func(ctx context.Context) error
}

// Scheduled is implemented by modules with periodic tasks.
type Scheduled interface {
	Schedules() []Schedule
}
