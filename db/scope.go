package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// applyScope sets search_path and ms.app_id on conn in a single batch round trip.
func applyScope(ctx context.Context, conn *pgxpool.Conn, schema string) error {
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
	return nil
}

// resetScope clears search_path and ms.app_id in a single batch round trip.
// If reset fails, the connection is destroyed rather than returned dirty to the pool.
func resetScope(conn *pgxpool.Conn) {
	batch := &pgx.Batch{}
	batch.Queue("RESET search_path")
	batch.Queue("RESET ms.app_id")

	br := conn.SendBatch(context.Background(), batch)
	_, err1 := br.Exec()
	_, err2 := br.Exec()
	br.Close()

	if err1 != nil || err2 != nil {
		// Connection is dirty — destroy instead of returning to pool
		conn.Conn().Close(context.Background())
	}
}
