package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// applyScope sets search_path and ms.app_id on conn in a single batch round trip.
//
// If local is true, both are set as transaction-local (SET LOCAL / set_config
// is_local=true) so they are automatically cleared on COMMIT or ROLLBACK. The
// caller MUST be inside an active transaction — SET LOCAL outside a tx is a
// silent no-op. Use local=true from Tx() and local=false from AcquireScoped.
//
// Cleanup on release is handled by the pool's AfterRelease hook (afterReleaseReset).
//
// Postgres custom GUC requirement:
// `ms.app_id` is a custom GUC under the `ms.*` namespace. Postgres auto-creates
// custom GUCs on first set_config call, so no postgresql.conf change is needed
// for normal operation. RLS policies should reference the value via
// `current_setting('ms.app_id', true)` (the second argument suppresses the
// "unrecognized configuration parameter" error during the brief window between
// Acquire and applyScope on a fresh connection).
func applyScope(ctx context.Context, conn *pgxpool.Conn, schema string, local bool) error {
	sanitized := pgx.Identifier{schema}.Sanitize()

	batch := &pgx.Batch{}
	if local {
		batch.Queue("SET LOCAL search_path TO " + sanitized)
	} else {
		batch.Queue("SET search_path TO " + sanitized)
	}
	batch.Queue("SELECT set_config('ms.app_id', $1, $2)", schema, local)

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
	return nil
}
