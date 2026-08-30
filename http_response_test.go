package mirrorstack_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ms "github.com/mirrorstack-ai/app-module-sdk"
)

func TestWriteServerErrorDoesNotExposeInternalError(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	ms.WriteServerError(recorder, request, "read secret record", errors.New("database password leaked"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if strings.Contains(recorder.Body.String(), "password") || strings.Contains(recorder.Body.String(), "secret record") {
		t.Fatalf("response exposed internal detail: %s", recorder.Body.String())
	}
	if got := recorder.Body.String(); got != "{\"error\":\"request could not be completed\"}\n" {
		t.Fatalf("body = %q", got)
	}
}
