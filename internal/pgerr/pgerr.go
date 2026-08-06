// Package pgerr classifies the Postgres error codes the SDK's own
// infrastructure DDL has to interpret rather than propagate. It exists because
// both internal/core (the dev dependency directory) and internal/contributions
// (the per-app contribution store) create tables lazily and must read the same
// two verdicts out of a driver error the same way.
package pgerr

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// RelationAlreadyExists reports whether err is Postgres telling us the relation
// we tried to create is already there. 42P07 is the direct duplicate_table
// verdict; 23505 is the same race observed one layer down, as a unique
// violation on the pg_class/pg_type catalog index, and is what
// CREATE TABLE IF NOT EXISTS actually raises when two backends interleave
// between the existence check and the insert.
//
// Both outcomes mean THE RELATION EXISTS, which is the whole postcondition a
// create-if-absent helper owes its caller, so callers treat both as success.
func RelationAlreadyExists(err error) bool {
	return hasCode(err, "42P07", "23505")
}

// UndefinedTable reports whether err is Postgres 42P01 — the statement named a
// relation that does not exist. For a table the SDK provisions per app, this is
// the one error that means "never provisioned for this app" rather than "the
// query is wrong", so a caller can create the table and retry instead of
// failing a request it has no other way to satisfy.
func UndefinedTable(err error) bool {
	return hasCode(err, "42P01")
}

func hasCode(err error, codes ...string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	for _, c := range codes {
		if pgErr.Code == c {
			return true
		}
	}
	return false
}
