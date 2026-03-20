package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/handler"
)

// extractCtx runs a request through ExtractContext and captures the result.
func extractCtx(t *testing.T, r *http.Request) context.Context {
	t.Helper()
	var captured context.Context
	handler.ExtractContext(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = r.Context()
	})).ServeHTTP(httptest.NewRecorder(), r)
	if captured == nil {
		t.Fatal("ExtractContext did not call next handler")
	}
	return captured
}

func TestExtractContext_AllHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set(handler.HeaderAppID, "app-123")
	r.Header.Set(handler.HeaderSchemaName, "app_x7k2")
	r.Header.Set(handler.HeaderAppPublicID, "app_clinic")
	r.Header.Set(handler.HeaderRequestID, "req-789")
	r.Header.Set(handler.HeaderPlatformUserID, "platform-user-1")
	r.Header.Set(handler.HeaderPlatformUserPublicID, "usr_plat")
	r.Header.Set(handler.HeaderModuleID, "video")
	r.Header.Set(handler.HeaderAuthType, handler.AuthTypePlatform)

	ctx := extractCtx(t, r)

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"AppID", handler.GetAppID(ctx), "app-123"},
		{"SchemaName", handler.GetSchemaName(ctx), "app_x7k2"},
		{"AppPublicID", handler.GetAppPublicID(ctx), "app_clinic"},
		{"RequestID", handler.GetRequestID(ctx), "req-789"},
		{"PlatformUserID", handler.GetPlatformUserID(ctx), "platform-user-1"},
		{"PlatformUserPublicID", handler.GetPlatformUserPublicID(ctx), "usr_plat"},
		{"ModuleID", handler.GetModuleID(ctx), "video"},
		{"AuthType", handler.GetAuthType(ctx), handler.AuthTypePlatform},
	}

	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

func TestExtractContext_NoHeaders_ReturnsEmpty(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	ctx := extractCtx(t, r)

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

func TestExtractContext_PartialHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set(handler.HeaderAppID, "app-only")

	ctx := extractCtx(t, r)

	if got := handler.GetAppID(ctx); got != "app-only" {
		t.Errorf("AppID: got %q, want %q", got, "app-only")
	}
	if got := handler.GetSchemaName(ctx); got != "" {
		t.Errorf("SchemaName: expected empty, got %q", got)
	}
}

func TestExtractContext_PreservesExistingContext(t *testing.T) {
	type customKey string
	const testKey customKey = "test"

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set(handler.HeaderAppID, "app-123")
	r = r.WithContext(context.WithValue(r.Context(), testKey, "original-value"))

	ctx := extractCtx(t, r)

	if got := handler.GetAppID(ctx); got != "app-123" {
		t.Errorf("AppID: got %q, want %q", got, "app-123")
	}
	if got, ok := ctx.Value(testKey).(string); !ok || got != "original-value" {
		t.Errorf("pre-existing context value lost: got %q", got)
	}
}

func TestGetters_RawContext_ReturnsEmpty(t *testing.T) {
	ctx := context.Background()

	if got := handler.GetAppID(ctx); got != "" {
		t.Errorf("AppID from raw context: expected empty, got %q", got)
	}
}

// --- NewContext ---

func TestNewContext_AllFields(t *testing.T) {
	ctx := handler.NewContext(context.Background(), handler.ContextParams{
		AppID:       "app-ecs",
		SchemaName:  "app_ecs_schema",
		AppPublicID: "app_clinic",
		RequestID:   "req-task-1",
		ModuleID:    "video",
		AuthType:    handler.AuthTypeInternal,
	})

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"AppID", handler.GetAppID(ctx), "app-ecs"},
		{"SchemaName", handler.GetSchemaName(ctx), "app_ecs_schema"},
		{"AppPublicID", handler.GetAppPublicID(ctx), "app_clinic"},
		{"RequestID", handler.GetRequestID(ctx), "req-task-1"},
		{"ModuleID", handler.GetModuleID(ctx), "video"},
		{"AuthType", handler.GetAuthType(ctx), handler.AuthTypeInternal},
	}

	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

func TestNewContext_MarksExtracted(t *testing.T) {
	ctx := handler.NewContext(context.Background(), handler.ContextParams{
		AppID: "app-1",
	})

	// RequireInternal should not reject — context is marked as extracted.
	h := handler.RequireInternal(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Build a request with the NewContext context + internal auth type.
	ctx = handler.NewContext(ctx, handler.ContextParams{
		AppID:    "app-1",
		AuthType: handler.AuthTypeInternal,
	})
	r := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (context marked as extracted), got %d", w.Code)
	}
}

func TestNewContext_PartialFields(t *testing.T) {
	ctx := handler.NewContext(context.Background(), handler.ContextParams{
		AppID:    "app-only",
		ModuleID: "video",
	})

	if got := handler.GetAppID(ctx); got != "app-only" {
		t.Errorf("AppID: got %q", got)
	}
	if got := handler.GetSchemaName(ctx); got != "" {
		t.Errorf("SchemaName: expected empty, got %q", got)
	}
	if got := handler.GetModuleID(ctx); got != "video" {
		t.Errorf("ModuleID: got %q", got)
	}
}

func TestNewContext_PreservesParent(t *testing.T) {
	type customKey string
	const testKey customKey = "test"

	parent := context.WithValue(context.Background(), testKey, "parent-value")
	ctx := handler.NewContext(parent, handler.ContextParams{
		AppID: "app-1",
	})

	if got := handler.GetAppID(ctx); got != "app-1" {
		t.Errorf("AppID: got %q", got)
	}
	if got, ok := ctx.Value(testKey).(string); !ok || got != "parent-value" {
		t.Errorf("parent value lost: got %q", got)
	}
}
