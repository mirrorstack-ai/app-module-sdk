package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Tx runs fn inside a transaction. Commits on success, rolls back on error or panic.
// Sets search_path and ms.app_id before BEGIN, resets after.
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
	defer func() {
		resetScope(conn)
		conn.Release()
	}()

	schema := SchemaFrom(ctx)
	if schema != "" {
		if err := applyScope(ctx, conn, schema); err != nil {
			return err
		}
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mirrorstack/db: failed to begin transaction: %w", err)
	}

	defer func() {
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
