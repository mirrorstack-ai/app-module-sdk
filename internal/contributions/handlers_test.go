package contributions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mirrorstack-ai/app-module-sdk/db"
)

type handlerQuerier struct {
	execArgs [][]any
}

func (q *handlerQuerier) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	q.execArgs = append(q.execArgs, args)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (*handlerQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (*handlerQuerier) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func contributionHandlersForTest(t *testing.T, q *handlerQuerier, opens *int) *Handlers {
	t.Helper()
	registry := NewRegistry()
	if err := registry.Define(NewSlot("profiles", "testPayload", nil, func(data json.RawMessage) error {
		var payload struct {
			Name string `json:"name"`
		}
		return json.Unmarshal(data, &payload)
	})); err != nil {
		t.Fatal(err)
	}
	return NewHandlers(registry, NewStorage("mhost"), func(*http.Request) (db.Querier, func(), error) {
		*opens = *opens + 1
		return q, func() {}, nil
	})
}

func TestRegisterCanonicalizesOwnerModuleID(t *testing.T) {
	t.Parallel()

	q := &handlerQuerier{}
	opens := 0
	handlers := contributionHandlersForTest(t, q, &opens)
	req := httptest.NewRequest(http.MethodPost, "/profiles/m123E4567E89B62D3A456426614174000", strings.NewReader(`{"name":"Ada"}`))
	recorder := httptest.NewRecorder()
	handlers.Routes().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if opens != 1 || len(q.execArgs) != 1 {
		t.Fatalf("DB opens=%d execs=%d, want 1 each", opens, len(q.execArgs))
	}
	if got := q.execArgs[0][1]; got != "123e4567-e89b-62d3-a456-426614174000" {
		t.Fatalf("stored owner = %q, want canonical UUID", got)
	}
	if !strings.Contains(recorder.Body.String(), `"id":"123e4567-e89b-62d3-a456-426614174000"`) {
		t.Fatalf("response did not return canonical ID: %s", recorder.Body.String())
	}
}

func TestRegisterRejectsMalformedOwnerModuleIDBeforeDB(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"contributor", "m123", "123e4567-e89b-12d3-a456-42661417400g"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			q := &handlerQuerier{}
			opens := 0
			handlers := contributionHandlersForTest(t, q, &opens)
			req := httptest.NewRequest(http.MethodPost, "/profiles/"+id, strings.NewReader(`{"name":"Ada"}`))
			recorder := httptest.NewRecorder()
			handlers.Routes().ServeHTTP(recorder, req)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
			}
			if opens != 0 || len(q.execArgs) != 0 {
				t.Fatalf("malformed ID reached DB: opens=%d execs=%d", opens, len(q.execArgs))
			}
		})
	}
}

func TestUnregisterCanonicalizesOwnerModuleID(t *testing.T) {
	t.Parallel()

	q := &handlerQuerier{}
	opens := 0
	handlers := contributionHandlersForTest(t, q, &opens)
	req := httptest.NewRequest(http.MethodDelete, "/profiles/m123e4567e89b12d3a456426614174000", nil)
	recorder := httptest.NewRecorder()
	handlers.Routes().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if opens != 1 || len(q.execArgs) != 1 {
		t.Fatalf("DB opens=%d execs=%d, want 1 each", opens, len(q.execArgs))
	}
	if got := q.execArgs[0][1]; got != "123e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("deleted owner = %q, want canonical UUID", got)
	}
}
