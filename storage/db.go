package storage

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mirrorstack-ai/app-module-sdk/meter"
)

var validSchema = regexp.MustCompile(`\Aapp_[a-z0-9_]{1,63}\z`)

// WithSchema runs fn inside a transaction with search_path set to the app's schema.
// If a Meter is present in the context, auto-tracks db_duration_ms.
func WithSchema[T any](ctx context.Context, pool *pgxpool.Pool, schema string, fn func(tx pgx.Tx) (T, error)) (T, error) {
	var zero T
	if !validSchema.MatchString(schema) {
		return zero, fmt.Errorf("storage: invalid schema name %q", schema)
	}

	start := time.Now()

	tx, err := pool.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := SetSearchPath(ctx, tx, schema); err != nil {
		return zero, err
	}

	result, err := fn(tx)
	if err != nil {
		return zero, err
	}

	if err := tx.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit: %w", err)
	}

	if m := meter.FromContext(ctx); m != nil {
		m.Track("db_duration_ms", float64(time.Since(start).Milliseconds()))
		m.Track("db_queries", 1)
	}

	return result, nil
}

// SetSearchPath sets search_path on an existing transaction.
func SetSearchPath(ctx context.Context, tx pgx.Tx, schema string) error {
	if !validSchema.MatchString(schema) {
		return fmt.Errorf("storage: invalid schema name %q", schema)
	}
	_, err := tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{schema}.Sanitize())
	if err != nil {
		return fmt.Errorf("set search_path: %w", err)
	}
	return nil
}
