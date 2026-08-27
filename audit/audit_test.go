package audit

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type recordingQuerier struct {
	sql  string
	args []any
	err  error
}

func (q *recordingQuerier) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	q.sql, q.args = sql, args
	return pgconn.CommandTag{}, q.err
}

// Record only ever Execs. These exist to satisfy db.Querier, and they panic
// rather than return zero values: a Record that started reading would be a
// change worth failing loudly on, not one to discover through a nil row.
func (q *recordingQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("audit.Record must not query")
}

func (q *recordingQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("audit.Record must not query")
}

// 🔴 Entry MUST have no way to name an actor.
//
// This is why audit moved to the platform at all. Every per-module table it
// replaces took the actor from whatever the caller supplied — one module writes
// a URL path parameter as the actor on an identity-free internal surface, so
// its trail records whoever the CALLER SAID it was, which is not an audit trail.
//
// A doc comment asking people not to pass an actor is a request. A struct with
// no such field is a guarantee, and this test is what keeps it one: adding an
// Actor or UserID field "because it would be convenient" fails here.
func TestEntryCannotCarryAnActor(t *testing.T) {
	t.Parallel()
	entry := reflect.TypeOf(Entry{})
	for i := range entry.NumField() {
		name := strings.ToLower(entry.Field(i).Name)
		if strings.Contains(name, "actor") || name == "userid" || name == "by" || name == "who" {
			t.Errorf("Entry.%s lets a caller name the actor — the platform stamps it from the "+
				"authenticated identity, and a caller-supplied actor must stay unexpressible",
				entry.Field(i).Name)
		}
	}
}

// An entry with no subject or no action produces a row nothing can render and
// nothing can search.
func TestRecordRejectsIncompleteEntries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		entry Entry
	}{
		{"no subject kind", Entry{SubjectID: "u1", Action: "granted"}},
		{"no subject id", Entry{SubjectKind: "user", Action: "granted"}},
		{"no action", Entry{SubjectKind: "user", SubjectID: "u1"}},
		{"whitespace is not a value", Entry{SubjectKind: " ", SubjectID: "u1", Action: "granted"}},
	} {
		q := &recordingQuerier{}
		if err := Record(context.Background(), q, tc.entry); !errors.Is(err, ErrInvalidEntry) {
			t.Errorf("%s: err = %v, want ErrInvalidEntry", tc.name, err)
		}
		if q.sql != "" {
			t.Errorf("%s: an invalid entry still reached the database", tc.name)
		}
	}
}

// The details cap is enforced HERE, inside the caller's transaction, where the
// module can still do something about it — rather than later in a drain, where
// the only options are dropping the row or retrying it forever.
func TestRecordRejectsOversizedDetails(t *testing.T) {
	t.Parallel()
	q := &recordingQuerier{}
	err := Record(context.Background(), q, Entry{
		SubjectKind: "user", SubjectID: "u1", Action: "granted",
		Details: strings.Repeat("x", maxDetailBytes+1),
	})
	if err == nil {
		t.Fatal("an oversized details blob was accepted")
	}
	if q.sql != "" {
		t.Error("an oversized entry still reached the database")
	}
}

// A valid entry writes to the OUTBOX, on the caller's own querier — that is
// what makes the audit row atomic with the change it describes.
func TestRecordWritesToTheOutbox(t *testing.T) {
	t.Parallel()
	q := &recordingQuerier{}
	err := Record(context.Background(), q, Entry{
		SubjectKind: "credit-account", SubjectID: "u1", Action: "debited",
		Details: map[string]any{"amount": 50},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !strings.Contains(q.sql, "_audit_outbox") {
		t.Errorf("Record did not target the outbox: %s", q.sql)
	}
	if !strings.Contains(q.sql, "INSERT") {
		t.Errorf("Record is not an append: %s", q.sql)
	}
	if len(q.args) != 4 {
		t.Fatalf("args = %d, want 4 (kind, id, action, details)", len(q.args))
	}
	// Details are serialised here rather than at drain time, so a module that
	// hands over something unserialisable finds out inside its own transaction.
	details, ok := q.args[3].([]byte)
	if !ok {
		t.Fatalf("details arg = %T, want []byte", q.args[3])
	}
	var round map[string]any
	if err := json.Unmarshal(details, &round); err != nil {
		t.Fatalf("details are not valid JSON: %v", err)
	}
	if round["amount"] != float64(50) {
		t.Errorf("details round trip = %v", round)
	}
}
