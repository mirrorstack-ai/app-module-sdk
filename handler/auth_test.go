package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/handler"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func withHeaders(t *testing.T, headers map[string]string) *http.Request {
	t.Helper()
	r := httptest.NewRequest("GET", "/", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	var captured *http.Request
	handler.ExtractContext(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = r
	})).ServeHTTP(w, r)
	if captured == nil {
		t.Fatal("ExtractContext did not call next handler; captured request is nil")
	}
	return captured
}

// platformHeaders returns headers for a valid platform user.
func platformHeaders(userID string) map[string]string {
	return map[string]string{
		handler.HeaderPlatformUserID: userID,
		handler.HeaderAuthType:       handler.AuthTypePlatform,
	}
}

// --- RequirePlatformUser ---

func TestRequirePlatformUser_NoHeaders_Returns403(t *testing.T) {
	h := handler.RequirePlatformUser()(okHandler())
	r := withHeaders(t, map[string]string{})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
	assertErrorBody(t, w, "forbidden", "platform access required")
}

func TestRequirePlatformUser_ValidUser_Returns200(t *testing.T) {
	h := handler.RequirePlatformUser()(okHandler())
	r := withHeaders(t, platformHeaders("platform-user-123"))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestRequirePlatformUser_WithPermission_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when passing permission arguments")
		}
	}()
	handler.RequirePlatformUser(handler.PlatformAdmin)
}

// --- RequireInternal ---

func TestRequireInternal_ValidAuth_Returns200(t *testing.T) {
	h := handler.RequireInternal(okHandler())
	r := withHeaders(t, map[string]string{
		handler.HeaderAuthType: handler.AuthTypeInternal,
	})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestRequireInternal_WrongAuthType_Returns403(t *testing.T) {
	h := handler.RequireInternal(okHandler())
	r := withHeaders(t, map[string]string{
		handler.HeaderAuthType: handler.AuthTypePlatform,
	})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestRequireInternal_NoExtractContext_Returns403(t *testing.T) {
	h := handler.RequireInternal(okHandler())
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set(handler.HeaderAuthType, handler.AuthTypeInternal)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 without ExtractContext, got %d", w.Code)
	}
}

func TestRequirePlatformUser_EmptyUserID_Returns403(t *testing.T) {
	h := handler.RequirePlatformUser()(okHandler())
	r := withHeaders(t, map[string]string{
		handler.HeaderPlatformUserID: "",
		handler.HeaderAuthType:       handler.AuthTypePlatform,
	})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for empty string, got %d", w.Code)
	}
}

func TestRequirePlatformUser_WhitespaceUserID_Returns403(t *testing.T) {
	h := handler.RequirePlatformUser()(okHandler())
	r := withHeaders(t, map[string]string{
		handler.HeaderPlatformUserID: "   ",
		handler.HeaderAuthType:       handler.AuthTypePlatform,
	})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for whitespace-only user ID, got %d", w.Code)
	}
}

func TestRequirePlatformUser_WrongAuthType_Returns403(t *testing.T) {
	h := handler.RequirePlatformUser()(okHandler())
	r := withHeaders(t, map[string]string{
		handler.HeaderPlatformUserID: "user-123",
		handler.HeaderAuthType:       "client", // wrong type — not platform
	})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for wrong auth type, got %d", w.Code)
	}
}

func TestRequirePlatformUser_MissingAuthType_Returns403(t *testing.T) {
	h := handler.RequirePlatformUser()(okHandler())
	r := withHeaders(t, map[string]string{
		handler.HeaderPlatformUserID: "user-123",
		// no AuthType header
	})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for missing auth type, got %d", w.Code)
	}
}

func TestRequirePlatformUser_WithoutExtractContext_Returns403(t *testing.T) {
	h := handler.RequirePlatformUser()(okHandler())
	// Deliberately bypass withHeaders — raw request, no ExtractContext
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set(handler.HeaderPlatformUserID, "user-123")
	r.Header.Set(handler.HeaderAuthType, handler.AuthTypePlatform)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 without ExtractContext, got %d", w.Code)
	}
}

// --- ExtractContext ---

func TestExtractContext_ReadsAllHeaders(t *testing.T) {
	r := withHeaders(t, map[string]string{
		handler.HeaderAppID:                "app-123",
		handler.HeaderSchemaName:           "app_x7k2",
		handler.HeaderAppPublicID:          "app_clinic",
		handler.HeaderRequestID:            "req-789",
		handler.HeaderPlatformUserID:       "platform-789",
		handler.HeaderPlatformUserPublicID: "usr_platform",
		handler.HeaderModuleID:             "video",
		handler.HeaderAuthType:             handler.AuthTypePlatform,
	})

	ctx := r.Context()

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"AppID", handler.GetAppID(ctx), "app-123"},
		{"SchemaName", handler.GetSchemaName(ctx), "app_x7k2"},
		{"AppPublicID", handler.GetAppPublicID(ctx), "app_clinic"},
		{"RequestID", handler.GetRequestID(ctx), "req-789"},
		{"PlatformUserID", handler.GetPlatformUserID(ctx), "platform-789"},
		{"PlatformUserPublicID", handler.GetPlatformUserPublicID(ctx), "usr_platform"},
		{"ModuleID", handler.GetModuleID(ctx), "video"},
		{"AuthType", handler.GetAuthType(ctx), handler.AuthTypePlatform},
	}

	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

func TestExtractContext_MissingHeaders_ReturnsEmpty(t *testing.T) {
	r := withHeaders(t, map[string]string{})
	ctx := r.Context()

	tests := []struct {
		name string
		got  string
	}{
		{"AppID", handler.GetAppID(ctx)},
		{"SchemaName", handler.GetSchemaName(ctx)},
		{"AppPublicID", handler.GetAppPublicID(ctx)},
		{"RequestID", handler.GetRequestID(ctx)},
		{"PlatformUserID", handler.GetPlatformUserID(ctx)},
		{"PlatformUserPublicID", handler.GetPlatformUserPublicID(ctx)},
		{"ModuleID", handler.GetModuleID(ctx)},
		{"AuthType", handler.GetAuthType(ctx)},
	}

	for _, tt := range tests {
		if tt.got != "" {
			t.Errorf("%s: expected empty, got %q", tt.name, tt.got)
		}
	}
}

// --- Helpers ---

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func assertErrorBody(t *testing.T, w *httptest.ResponseRecorder, code, message string) {
	t.Helper()
	var resp errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode error body: %v", err)
	}
	if resp.Error.Code != code {
		t.Errorf("error code: got %q, want %q", resp.Error.Code, code)
	}
	if resp.Error.Message != message {
		t.Errorf("error message: got %q, want %q", resp.Error.Message, message)
	}
}
