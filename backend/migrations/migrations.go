// Package migrations embeds the goose SQL migrations.
package migrations

import (
	"context"
	"database/sql"
	"embed"

	"github.com/pressly/goose/v3"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose
)

//go:embed *.sql
var FS embed.FS

// Up applies all pending migrations.
func Up(ctx context.Context, url string) error {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return err
	}
	defer db.Close()
	goose.SetBaseFS(FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	goose.SetLogger(goose.NopLogger())
	return goose.UpContext(ctx, db, ".")
}

// Status returns the current schema version.
func Status(ctx context.Context, url string) (int64, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	goose.SetBaseFS(FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return 0, err
	}
	return goose.GetDBVersionContext(ctx, db)
}
