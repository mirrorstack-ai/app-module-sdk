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

// TxReadOnly runs fn inside a READ ONLY transaction (pgx.TxOptions with
// AccessMode: pgx.ReadOnly). Postgres rejects any write attempted inside fn
// with SQLSTATE 25006 regardless of what the connecting role is granted — the
// doubled enforcement the deployed cross-module read runs under (decision 18
// §2 invariant 2: consumer-role connection + READ ONLY tx). The read executes
// AS whatever role owns pool, so the install-time GRANT is the ceiling.
//
// Unlike Tx, fn receives the raw pgx.Tx: the dynamic-SELECT executor needs
// tx.Query + pgx.CollectRows, which the Querier interface does not expose. The
// app schema (search_path + ms.app_id) is pinned transaction-local from ctx
// (WithSchema) so SET LOCAL auto-clears on COMMIT/ROLLBACK and RLS on the
// producer's exposed relation resolves to this tenant — SET LOCAL is legal in
// a read-only tx. The pool's AfterRelease hook is the defense-in-depth backstop.
func TxReadOnly(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("mirrorstack/db: failed to begin read-only transaction: %w", err)
	}

	// Panic recovery before any query so a panic still rolls back. safeRollback
	// swallows a rollback failure so it cannot mask the original panic.
	defer func() {
		if p := recover(); p != nil {
			safeRollback(tx)
			panic(p)
		}
	}()

	schema := SchemaFrom(ctx)
	if schema != "" {
		if err := setScopeLocalTx(ctx, tx, schema); err != nil {
			safeRollback(tx)
			return err
		}
	}

	if err := fn(tx); err != nil {
		safeRollback(tx)
		return err
	}

	// Read-only tx: nothing to persist, but Commit still ends the tx cleanly
	// and returns the connection to the pool. Background ctx so a canceled
	// request ctx cannot turn the clean finish into a spurious error.
	return tx.Commit(context.Background())
}

// setScopeLocalTx pins search_path + ms.app_id transaction-local on an
// already-begun tx, mirroring applyScope(local=true) but for a pgx.Tx obtained
// via pool.BeginTx (applyScope needs a *pgxpool.Conn). SET LOCAL / set_config
// is_local=true auto-clear on COMMIT/ROLLBACK. The schema is Sanitize()d before
// it reaches SQL text; the GUC value is bound as $1.
func setScopeLocalTx(ctx context.Context, tx pgx.Tx, schema string) error {
	sanitized := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+sanitized); err != nil {
		return fmt.Errorf("mirrorstack/db: failed to set search_path: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('ms.app_id', $1, true)", schema); err != nil {
		return fmt.Errorf("mirrorstack/db: failed to set ms.app_id: %w", err)
	}
	return nil
}
