// Package db provides multi-tenant PostgreSQL access with schema-per-app isolation.
//
// In production, the platform sets DATABASE_URL and injects X-MS-App-Schema
// via trusted headers. The SDK sets search_path automatically per request.
//
// In dev, connects to local postgres (default: localhost:5433) with no schema isolation.
package db

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultDevURL = "postgres://postgres:postgres@localhost:5433/module?sslmode=disable"
	schemaKey     = contextKey("ms-app-schema")
)

type contextKey string

// Querier is the interface for database operations.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DB is the database client.
type DB struct {
	pool *pgxpool.Pool
}

// Open creates a DB from DATABASE_URL env var, falling back to local dev default.
func Open(ctx context.Context) (*DB, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = defaultDevURL
	}
	return New(ctx, url)
}

// New creates a DB with the given connection string.
func New(ctx context.Context, connStr string) (*DB, error) {
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/db: failed to connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("mirrorstack/db: failed to ping: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Close closes the connection pool.
func (d *DB) Close() {
	if d.pool != nil {
		d.pool.Close()
	}
}

// Pool returns the underlying pgxpool.Pool.
func (d *DB) Pool() *pgxpool.Pool {
	return d.pool
}

// WithSchema returns a context with the app schema set.
func WithSchema(ctx context.Context, schema string) context.Context {
	return context.WithValue(ctx, schemaKey, schema)
}

// SchemaFrom reads the app schema from the context.
func SchemaFrom(ctx context.Context) string {
	s, _ := ctx.Value(schemaKey).(string)
	return s
}

// Conn acquires a connection and sets search_path if a schema is in the context.
// The caller MUST call release when done. Release resets search_path to prevent
// schema leaking to the next pool borrower.
//
//	conn, release, err := db.Conn(ctx)
//	if err != nil { ... }
//	defer release()
//	conn.QueryRow(ctx, "SELECT ...").Scan(&v)
func (d *DB) Conn(ctx context.Context) (Querier, func(), error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("mirrorstack/db: failed to acquire connection: %w", err)
	}

	schema := SchemaFrom(ctx)
	if schema != "" {
		if _, err := conn.Exec(ctx, "SET search_path TO "+pgx.Identifier{schema}.Sanitize()); err != nil {
			conn.Release()
			return nil, nil, fmt.Errorf("mirrorstack/db: failed to set search_path: %w", err)
		}
	}

	// Reset search_path on release to prevent schema leaking to next borrower
	release := func() {
		if schema != "" {
			conn.Exec(context.Background(), "RESET search_path")
		}
		conn.Release()
	}

	return conn, release, nil
}

// Exec runs a query without returning rows. Sets search_path if schema is in context.
func (d *DB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	conn, release, err := d.Conn(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	defer release()
	return conn.Exec(ctx, sql, args...)
}
