package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// safeRollback runs tx.Rollback with a background context (the request ctx
// may be canceled mid-fn) and swallows any panic from inside the rollback
// itself (e.g., a network failure during ROLLBACK). Used in every rollback
// site so a rollback failure can never replace the caller's original error
// or panic with a misleading rollback error.
func safeRollback(tx pgx.Tx) {
	defer func() { _ = recover() }()
	_ = tx.Rollback(context.Background())
}

// Tx runs fn inside a transaction. Commits on success, rolls back on error or panic.
//
// Inside the transaction, search_path and ms.app_id are set transaction-local
// (SET LOCAL / set_config is_local=true) so they are automatically cleared on
// COMMIT or ROLLBACK. The pool's AfterRelease hook is the defense-in-depth backstop.
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

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mirrorstack/db: failed to begin transaction: %w", err)
	}

	// Panic recovery must be deferred BEFORE applyScope so a panic during
	// applyScope (or anywhere after Begin) still rolls back the transaction.
	// safeRollback ensures a rollback failure cannot swallow the original panic.
	defer func() {
		if p := recover(); p != nil {
			safeRollback(tx)
			panic(p)
		}
	}()

	schema := SchemaFrom(ctx)
	if schema != "" {
		// local=true: search_path and ms.app_id auto-clear on COMMIT/ROLLBACK.
		// Must run AFTER Begin — SET LOCAL outside a tx is a silent no-op.
		if err := applyScope(ctx, conn, schema, true); err != nil {
			safeRollback(tx)
			return err
		}
	}

	if err := fn(tx); err != nil {
		safeRollback(tx)
		return err
	}

	// Background context: a canceled request ctx during commit causes Postgres
	// to roll back, which is silent data loss from the caller's perspective.
	return tx.Commit(context.Background())
}
