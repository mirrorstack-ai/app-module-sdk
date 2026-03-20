package storage

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("storage: not found")

// GetModuleConfig retrieves JSON config for a module from app_modules table.
// Returns ErrNotFound if the app/module combination does not exist.
func GetModuleConfig(ctx context.Context, pool *pgxpool.Pool, appID, moduleID string) (json.RawMessage, error) {
	var config json.RawMessage
	err := pool.QueryRow(ctx,
		`SELECT config FROM application.app_modules WHERE app_id = $1 AND module_id = $2`,
		appID, moduleID,
	).Scan(&config)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return config, nil
}

// UpdateModuleConfig updates JSON config for a module in app_modules table.
// Returns ErrNotFound if the app/module combination does not exist.
func UpdateModuleConfig(ctx context.Context, pool *pgxpool.Pool, appID, moduleID string, config json.RawMessage) error {
	tag, err := pool.Exec(ctx,
		`UPDATE application.app_modules SET config = $1, updated_at = now() WHERE app_id = $2 AND module_id = $3`,
		config, appID, moduleID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
