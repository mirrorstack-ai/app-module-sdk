package system

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// noopSeedAcquirer is a SeedConnAcquirer stub that fails the test if invoked.
// Mirrors noopTxRunner in lifecycle_test.go: tests using it assert that
// SeedHandler rejects the request BEFORE ever trying to reach the database.
func noopSeedAcquirer(t *testing.T) SeedConnAcquirer {
	return func(ctx context.Context) (SeedConn, func(), error) {
		t.Helper()
		t.Errorf("SeedConnAcquirer unexpectedly called — test assumed the handler would reject before acquiring a connection")
		return nil, nil, nil
	}
}

func TestSeedHandler_MalformedBody_400(t *testing.T) {
	t.Parallel()

	h := SeedHandler(noopSeedAcquirer(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/seed", strings.NewReader(`{not json`)))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for malformed body", rec.Code)
	}
}

func TestSeedHandler_EmptyBody_400(t *testing.T) {
	t.Parallel()

	h := SeedHandler(noopSeedAcquirer(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/seed", nil))

	// Unlike install (whose body is entirely optional), every seed field is
	// required — an empty body decodes to io.EOF, which writeBodyDecodeError
	// maps to 400 same as any other malformed body.
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for empty body", rec.Code)
	}
}

func TestSeedHandler_OversizeBody_413(t *testing.T) {
	t.Parallel()

	// A single "data" field bigger than seedMaxBodyBytes must trip
	// http.MaxBytesReader before the JSON decoder ever finishes — the
	// acquirer must never be reached.
	huge := strings.Repeat("a", seedMaxBodyBytes+1024)
	body := `{"appId":"seedtest","table":"items","columns":["id"],"data":"` + huge + `","first":true}`

	h := SeedHandler(noopSeedAcquirer(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/seed", strings.NewReader(body)))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413 for oversize body", rec.Code)
	}
}

func TestSeedHandler_MissingTableOrColumns_400(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"missing table", `{"appId":"seedtest","columns":["id"],"data":"1\n","first":true}`},
		{"empty table", `{"appId":"seedtest","table":"","columns":["id"],"data":"1\n","first":true}`},
		{"missing columns", `{"appId":"seedtest","table":"items","data":"1\n","first":true}`},
		{"empty columns", `{"appId":"seedtest","table":"items","columns":[],"data":"1\n","first":true}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := SeedHandler(noopSeedAcquirer(t))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/seed", strings.NewReader(tc.body)))

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for body %q", rec.Code, tc.body)
			}
		})
	}
}

func TestSeedHandler_InvalidAppID_400(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"empty appId", `{"appId":"","table":"items","columns":["id"],"data":"1\n","first":true}`},
		{"appId with invalid chars", `{"appId":"not an id!","table":"items","columns":["id"],"data":"1\n","first":true}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := SeedHandler(noopSeedAcquirer(t))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/seed", strings.NewReader(tc.body)))

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for body %q", rec.Code, tc.body)
			}
		})
	}
}

func TestSeedHandler_AcquireError_500(t *testing.T) {
	t.Parallel()

	acquireErr := errAcquire{}
	h := SeedHandler(func(ctx context.Context) (SeedConn, func(), error) {
		return nil, nil, acquireErr
	})
	body := `{"appId":"seedtest","table":"items","columns":["id"],"data":"1\n","first":true}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/seed", strings.NewReader(body)))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when acquire fails", rec.Code)
	}
}

type errAcquire struct{}

func (errAcquire) Error() string { return "acquire failed" }
