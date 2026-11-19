package sdktest

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTablesMissingPrefix_Integration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		t.Skip("skipping: DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("configured DATABASE_URL is unavailable: %v", err)
	}

	const schema = "sdktest_tableprefix"
	ident := pgx.Identifier{schema}.Sanitize()
	drop := func() { _, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+ident+` CASCADE`) }
	drop()
	t.Cleanup(drop)
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA `+ident+`;
		CREATE TABLE `+ident+`.mymod_items (id int);
		CREATE TABLE `+ident+`.mymod_audit_outbox (id int);
		CREATE TABLE `+ident+`.schema_migrations (version text);
		CREATE TABLE `+ident+`.items (id int);
		CREATE TABLE `+ident+`.mymodx_things (id int);
		CREATE VIEW `+ident+`.report AS SELECT 1 AS n`); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := TablesMissingPrefix(ctx, pool, "mymod", schema)
	if err != nil {
		t.Fatalf("TablesMissingPrefix: %v", err)
	}
	// "mymodx_things" starts with "mymod" but not "mymod_": the underscore is
	// literal, not a LIKE wildcard. The view and schema_migrations are exempt.
	if want := []string{"items", "mymodx_things"}; !slices.Equal(got, want) {
		t.Errorf("offending = %v, want %v", got, want)
	}

	if _, err := pool.Exec(ctx, `DROP TABLE `+ident+`.items; DROP TABLE `+ident+`.mymodx_things`); err != nil {
		t.Fatalf("cleanup tables: %v", err)
	}
	// With the offenders gone the public helper passes (it would t.Errorf).
	ValidateTablePrefix(t, ctx, pool, "mymod", schema)
}
