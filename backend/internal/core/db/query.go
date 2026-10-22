package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// List runs a query and maps rows to T by `db` struct tags.
func List[T any](ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]T, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByName[T])
	if out == nil {
		out = []T{}
	}
	return out, MapErr(err)
}

// One runs a query and maps exactly one row to T.
func One[T any](ctx context.Context, tx pgx.Tx, sql string, args ...any) (T, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		var zero T
		return zero, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[T])
	return v, MapErr(err)
}

// Count returns a single integer.
func Count(ctx context.Context, tx pgx.Tx, sql string, args ...any) (int, error) {
	var n int
	err := tx.QueryRow(ctx, sql, args...).Scan(&n)
	return n, MapErr(err)
}
