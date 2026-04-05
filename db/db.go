// Package db provides multi-tenant PostgreSQL access with app-scoped credential
// isolation and row-level security.
//
// Production: credentials injected per invocation via Lambda payload.
// Dev: DATABASE_URL env var with localhost fallback.
package db

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultDevURL = "postgres://postgres:postgres@localhost:5433/module?sslmode=disable"

// Querier is the interface for database operations.
// Compatible with pgxpool.Conn and sqlc's DBTX interface.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DB is the dev-mode database client (single pool, no credential injection).
type DB struct {
	pool *pgxpool.Pool
}

// Open creates a DB from DATABASE_URL env var, falling back to local dev default.
// Use this for dev mode only. Production uses PoolCache with injected credentials.
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

// Conn acquires a scoped connection. Sets search_path and ms.app_id if schema is in context.
// Resets both on release.
func (d *DB) Conn(ctx context.Context) (Querier, func(), error) {
	return AcquireScoped(ctx, d.pool)
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
