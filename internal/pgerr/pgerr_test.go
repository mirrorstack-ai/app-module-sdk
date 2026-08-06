package pgerr

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestRelationAlreadyExists covers the two SQLSTATEs a concurrent
// CREATE TABLE IF NOT EXISTS can raise. Both mean the relation exists, which is
// the entire postcondition a create-if-absent helper owes its caller.
func TestRelationAlreadyExists(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"42P07 duplicate_table", &pgconn.PgError{Code: "42P07"}, true},
		{"23505 catalog unique violation", &pgconn.PgError{Code: "23505"}, true},
		{"wrapped 23505", fmt.Errorf("dev directory: %w", &pgconn.PgError{Code: "23505"}), true},
		{"42501 insufficient_privilege is NOT a race", &pgconn.PgError{Code: "42501"}, false},
		{"08006 connection failure is NOT a race", &pgconn.PgError{Code: "08006"}, false},
		{"42P01 undefined_table is the opposite verdict", &pgconn.PgError{Code: "42P01"}, false},
		{"non-pg error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RelationAlreadyExists(tc.err); got != tc.want {
				t.Errorf("RelationAlreadyExists(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestUndefinedTable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"42P01 undefined_table", &pgconn.PgError{Code: "42P01"}, true},
		{"wrapped 42P01", fmt.Errorf("list contributions: %w", &pgconn.PgError{Code: "42P01"}), true},
		{"42703 undefined_column is a different bug", &pgconn.PgError{Code: "42703"}, false},
		{"42501 insufficient_privilege must not be healed", &pgconn.PgError{Code: "42501"}, false},
		{"non-pg error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UndefinedTable(tc.err); got != tc.want {
				t.Errorf("UndefinedTable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
