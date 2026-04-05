package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Tx runs fn inside a transaction. Commits on success, rolls back on error or panic.
// The Querier passed to fn is a transaction — all reads and writes are atomic.
//
//	err := db.Tx(ctx, pool, func(q db.Querier) error {
//	    queries := generated.New(q)
//	    item, err := queries.GetItem(ctx, id)
//	    if err != nil { return err }
//	    return queries.DeductBalance(ctx, params)
//	})
func Tx(ctx context.Context, pool *pgxpool.Pool, fn func(q Querier) error) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("mirrorstack/db: failed to acquire connection: %w", err)
	}
	defer conn.Release()

	// Set search_path before starting transaction
	schema := SchemaFrom(ctx)
	if schema != "" {
		sanitized := pgx.Identifier{schema}.Sanitize()

		batch := &pgx.Batch{}
		batch.Queue("SET search_path TO " + sanitized)
		batch.Queue("SELECT set_config('ms.app_id', $1, false)", schema)

		br := conn.SendBatch(ctx, batch)
		_, err1 := br.Exec()
		_, err2 := br.Exec()
		br.Close()

		if err1 != nil {
			return fmt.Errorf("mirrorstack/db: failed to set search_path: %w", err1)
		}
		if err2 != nil {
			return fmt.Errorf("mirrorstack/db: failed to set ms.app_id: %w", err2)
		}
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mirrorstack/db: failed to begin transaction: %w", err)
	}

	defer func() {
		if schema != "" {
			conn.Exec(context.Background(), "RESET search_path")
			conn.Exec(context.Background(), "RESET ms.app_id")
		}
		if p := recover(); p != nil {
			tx.Rollback(ctx)
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		tx.Rollback(ctx)
		return err
	}

	return tx.Commit(ctx)
}
