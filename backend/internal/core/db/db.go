// Package db wraps PostgreSQL access. Every tenant-scoped statement runs inside a
// transaction that sets app.tenant_id, so row-level security policies isolate tenants.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/exaring/otelpgx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openspoor/openspoor/internal/core/telemetry"
)

// DB is the shared connection pool.
type DB struct {
	Pool *pgxpool.Pool
}

// ErrNotFound is returned when a row does not exist (or is hidden by RLS).
var ErrNotFound = errors.New("not found")

// ErrConflict is returned on unique violations.
var ErrConflict = errors.New("conflict")

// Open connects to PostgreSQL.
func Open(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if cfg.MaxConns < 16 {
		cfg.MaxConns = 16
	}
	if telemetry.Enabled() {
		cfg.ConnConfig.Tracer = otelpgx.NewTracer()
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{Pool: pool}, nil
}

// Close releases the pool.
func (d *DB) Close() { d.Pool.Close() }

// Tx runs fn in a transaction scoped to one tenant. RLS policies read app.tenant_id.
func (d *DB) Tx(ctx context.Context, tenant uuid.UUID, fn func(pgx.Tx) error) error {
	return d.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "select set_config('app.tenant_id', $1, true)", tenant.String()); err != nil {
			return err
		}
		return fn(tx)
	})
}

// OperatorTx runs fn with cross-tenant visibility. Only operator and system code paths may call it.
func (d *DB) OperatorTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return d.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "select set_config('app.operator', 'on', true)"); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (d *DB) tx(ctx context.Context, fn func(pgx.Tx) error) (err error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return
		}
		err = tx.Commit(ctx)
	}()
	return MapErr(fn(tx))
}

// MapErr converts driver errors into package sentinel errors.
func MapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrConflict, pg.ConstraintName)
	}
	return err
}
