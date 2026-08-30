package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mirrorstack-ai/app-module-sdk/internal/auditstate"
	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationwire"
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

func provenancedContext(t *testing.T) (context.Context, []byte) {
	t.Helper()
	raw, err := os.ReadFile("../invocation/testdata/invocation_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.TrimSpace(raw)
	trusted, err := invocationwire.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return invocationwire.WithContextAndProof(context.Background(), trusted, raw), raw
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
		if strings.Contains(name, "actor") || strings.Contains(name, "provenance") ||
			strings.Contains(name, "proof") || strings.Contains(name, "invocation") ||
			name == "userid" || name == "by" || name == "who" {
			t.Errorf("Entry.%s lets a caller name the actor or provenance — the platform stamps it from the "+
				"authenticated invocation, and caller-supplied identity must stay unexpressible",
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
		{"subject kind too long", Entry{SubjectKind: strings.Repeat("x", maxSubjectKindBytes+1), SubjectID: "u1", Action: "granted"}},
		{"subject id too long", Entry{SubjectKind: "user", SubjectID: strings.Repeat("x", maxSubjectIDBytes+1), Action: "granted"}},
		{"action too long", Entry{SubjectKind: "user", SubjectID: "u1", Action: strings.Repeat("x", maxActionBytes+1)}},
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

func TestRecordRejectsNonObjectDetails(t *testing.T) {
	t.Parallel()
	for _, details := range []any{"text", 42, []string{"not", "an", "object"}} {
		q := &recordingQuerier{}
		err := Record(context.Background(), q, Entry{
			SubjectKind: "user", SubjectID: "u1", Action: "updated", Details: details,
		})
		if err == nil || !strings.Contains(err.Error(), "JSON object") {
			t.Errorf("details %T error = %v", details, err)
		}
		if q.sql != "" {
			t.Errorf("details %T reached the database", details)
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
	ctx, proof := provenancedContext(t)
	q := &recordingQuerier{}
	tracked := auditstate.Track(q)
	err := Record(ctx, tracked, Entry{
		SubjectKind: "credit-account", SubjectID: "u1", Action: "debited",
		Details: map[string]any{"amount": 50},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !tracked.Recorded() {
		t.Fatal("successful Record did not mark its enclosing transaction")
	}
	if !strings.Contains(q.sql, "_audit_outbox") {
		t.Errorf("Record did not target the outbox: %s", q.sql)
	}
	if !strings.Contains(q.sql, "INSERT") {
		t.Errorf("Record is not an append: %s", q.sql)
	}
	if len(q.args) != 5 {
		t.Fatalf("args = %d, want 5 (kind, id, action, details, proof)", len(q.args))
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
	gotProof, ok := q.args[4].([]byte)
	if !ok || !bytes.Equal(gotProof, proof) {
		t.Fatalf("proof arg = %T %q, want exact authenticated invocation", q.args[4], gotProof)
	}
	gotProof[0] ^= 0xff
	if again := invocationwire.ProofFromContext(ctx); len(again) == 0 || again[0] == gotProof[0] {
		t.Fatal("outbox proof shared mutable context storage")
	}
}

func TestRecordDoesNotMarkFailedInsert(t *testing.T) {
	t.Parallel()
	ctx, _ := provenancedContext(t)
	sentinel := errors.New("database unavailable")
	q := &recordingQuerier{err: sentinel}
	tracked := auditstate.Track(q)
	err := Record(ctx, tracked, Entry{SubjectKind: "user", SubjectID: "u1", Action: "updated"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Record error = %v", err)
	}
	if tracked.Recorded() {
		t.Fatal("failed insert marked the transaction for a drain")
	}
}

func TestRecordRejectsMissingProvenance(t *testing.T) {
	t.Parallel()
	q := &recordingQuerier{}
	err := Record(context.Background(), q, Entry{
		SubjectKind: "user", SubjectID: "u1", Action: "updated",
	})
	if !errors.Is(err, ErrProvenanceUnavailable) {
		t.Fatalf("Record error = %v, want ErrProvenanceUnavailable", err)
	}
	if q.sql != "" {
		t.Fatal("a proofless audit entry reached the database")
	}
}

func TestEnsureTableIncludesDurableProofAwareUpgrade(t *testing.T) {
	t.Parallel()
	q := &recordingQuerier{}
	if err := EnsureTable(context.Background(), q); err != nil {
		t.Fatalf("EnsureTable: %v", err)
	}
	for _, fragment := range []string{
		"invocation_proof bytea",
		"FOR UPDATE", // Guard against accidentally moving claiming into Record's DDL.
		"missing_invocation_proof",
		"audit_proof_state_ck",
		"audit_entry_ck",
		"lease_token",
		"lease_expires_at",
		"quarantined_at",
		"DROP INDEX IF EXISTS",
	} {
		if fragment == "FOR UPDATE" {
			if strings.Contains(q.sql, fragment) {
				t.Fatalf("EnsureTable unexpectedly contains claim SQL %q", fragment)
			}
			continue
		}
		if !strings.Contains(q.sql, fragment) {
			t.Errorf("EnsureTable SQL missing %q", fragment)
		}
	}
}
