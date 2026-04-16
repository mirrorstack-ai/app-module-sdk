package system

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

func newMCPReg(t *testing.T) *registry.Registry {
	t.Helper()
	return registry.New()
}

func TestMCPToolsList_EmitsRegisteredTools(t *testing.T) {
	t.Parallel()

	reg := newMCPReg(t)
	reg.AddMCPTool(registry.MCPToolDecl{
		Name: "greet", Description: "Say hi",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`"hi"`), nil
		},
	})

	req := httptest.NewRequest("GET", "/tools/list", nil)
	rec := httptest.NewRecorder()
	MCPToolsListHandler(reg).ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Tools []MCPToolEntry `json:"tools"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(body.Tools))
	}
	if body.Tools[0].Name != "greet" || body.Tools[0].Description != "Say hi" {
		t.Errorf("tool = %+v", body.Tools[0])
	}
}

func TestMCPToolsCall_InvokesHandler(t *testing.T) {
	t.Parallel()

	reg := newMCPReg(t)
	reg.AddMCPTool(registry.MCPToolDecl{
		Name:        "echo",
		Description: "Echoes args",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return args, nil
		},
	})

	body := strings.NewReader(`{"name":"echo","args":{"hello":"world"}}`)
	req := httptest.NewRequest("POST", "/tools/call", body)
	rec := httptest.NewRecorder()
	MCPToolsCallHandler(reg).ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(resp.Result) != `{"hello":"world"}` {
		t.Errorf("result = %s, want echo of args", resp.Result)
	}
}

func TestMCPToolsCall_UnknownToolReturns404(t *testing.T) {
	t.Parallel()

	reg := newMCPReg(t)
	req := httptest.NewRequest("POST", "/tools/call", strings.NewReader(`{"name":"missing","args":{}}`))
	rec := httptest.NewRecorder()
	MCPToolsCallHandler(reg).ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestMCPToolsCall_BadJSONReturns400(t *testing.T) {
	t.Parallel()

	reg := newMCPReg(t)
	req := httptest.NewRequest("POST", "/tools/call", strings.NewReader(`{invalid`))
	rec := httptest.NewRecorder()
	MCPToolsCallHandler(reg).ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestMCPToolsCall_EmptyNameReturns400(t *testing.T) {
	t.Parallel()

	reg := newMCPReg(t)
	req := httptest.NewRequest("POST", "/tools/call", strings.NewReader(`{"args":{}}`))
	rec := httptest.NewRecorder()
	MCPToolsCallHandler(reg).ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestMCPToolsCall_HandlerErrorReturns500(t *testing.T) {
	t.Parallel()

	reg := newMCPReg(t)
	reg.AddMCPTool(registry.MCPToolDecl{
		Name: "boom", Description: "Always fails",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("handler exploded")
		},
	})
	req := httptest.NewRequest("POST", "/tools/call", strings.NewReader(`{"name":"boom","args":{}}`))
	rec := httptest.NewRecorder()
	MCPToolsCallHandler(reg).ServeHTTP(rec, req)

	if rec.Code != 500 {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestMCPToolsCall_InvalidArgsReturns400(t *testing.T) {
	t.Parallel()

	reg := newMCPReg(t)
	reg.AddMCPTool(registry.MCPToolDecl{
		Name: "strict", Description: "Wants specific args",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return nil, ErrInvalidArgs
		},
	})
	req := httptest.NewRequest("POST", "/tools/call", strings.NewReader(`{"name":"strict","args":{}}`))
	rec := httptest.NewRecorder()
	MCPToolsCallHandler(reg).ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Errorf("status = %d, want 400 for ErrInvalidArgs", rec.Code)
	}
}

func TestMCPResourcesList_EmitsRegistered(t *testing.T) {
	t.Parallel()

	reg := newMCPReg(t)
	reg.AddMCPResource(registry.MCPResourceDecl{
		Name: "status", Description: "Current module status",
		Handler: func(ctx context.Context) (json.RawMessage, error) {
			return json.RawMessage(`"ok"`), nil
		},
	})

	req := httptest.NewRequest("GET", "/resources/list", nil)
	rec := httptest.NewRecorder()
	MCPResourcesListHandler(reg).ServeHTTP(rec, req)

	var body struct {
		Resources []MCPResourceEntry `json:"resources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Resources) != 1 || body.Resources[0].Name != "status" {
		t.Errorf("resources = %+v", body.Resources)
	}
}

func TestMCPResourcesRead_ReturnsContent(t *testing.T) {
	t.Parallel()

	reg := newMCPReg(t)
	reg.AddMCPResource(registry.MCPResourceDecl{
		Name: "status", Description: "Status",
		Handler: func(ctx context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"healthy":true}`), nil
		},
	})

	req := httptest.NewRequest("GET", "/resources/read?name=status", nil)
	rec := httptest.NewRecorder()
	MCPResourcesReadHandler(reg).ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if string(resp.Content) != `{"healthy":true}` {
		t.Errorf("content = %s", resp.Content)
	}
}

func TestMCPResourcesRead_MissingNameReturns400(t *testing.T) {
	t.Parallel()

	reg := newMCPReg(t)
	req := httptest.NewRequest("GET", "/resources/read", nil)
	rec := httptest.NewRecorder()
	MCPResourcesReadHandler(reg).ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestMCPResourcesRead_UnknownReturns404(t *testing.T) {
	t.Parallel()

	reg := newMCPReg(t)
	req := httptest.NewRequest("GET", "/resources/read?name=missing", nil)
	rec := httptest.NewRecorder()
	MCPResourcesReadHandler(reg).ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestManifest_IncludesMCP(t *testing.T) {
	t.Parallel()

	reg := registry.New()
	reg.AddMCPTool(registry.MCPToolDecl{
		Name: "greet", Description: "Say hi",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	})
	reg.AddMCPResource(registry.MCPResourceDecl{
		Name: "status", Description: "Status",
	})

	req := httptest.NewRequest("GET", "/__mirrorstack/platform/manifest", nil)
	rec := httptest.NewRecorder()
	ManifestHandler("demo", "Demo", "box", nil, nil, reg).ServeHTTP(rec, req)

	var got ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.MCP.Tools) != 1 || got.MCP.Tools[0].Name != "greet" {
		t.Errorf("manifest.mcp.tools = %+v", got.MCP.Tools)
	}
	if len(got.MCP.Resources) != 1 || got.MCP.Resources[0].Name != "status" {
		t.Errorf("manifest.mcp.resources = %+v", got.MCP.Resources)
	}

	// Handlers must not leak on the wire.
	if strings.Contains(rec.Body.String(), `"Handler"`) || strings.Contains(rec.Body.String(), `"handler"`) {
		t.Errorf("manifest body leaks handler field: %s", rec.Body.String())
	}
}

func TestManifest_EmptyMCPIsEmptyArrays(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "/__mirrorstack/platform/manifest", nil)
	rec := httptest.NewRecorder()
	ManifestHandler("demo", "Demo", "box", nil, nil, registry.New()).ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `"mcp":{"tools":[],"resources":[]}`) {
		t.Errorf("empty mcp should serialize as {tools:[],resources:[]}, got: %s", body)
	}
}

// Ensure the HTTP status code is emitted BEFORE any body write. Seen failures
// where an earlier http.Error() call had already sent a 500 would silently
// cascade; guard the happy path explicitly.
func TestMCPToolsCall_200HasCorrectContentType(t *testing.T) {
	t.Parallel()

	reg := newMCPReg(t)
	reg.AddMCPTool(registry.MCPToolDecl{
		Name: "ping", Description: "Pong",
		InputSchema: json.RawMessage(`{}`),
		Handler: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`"pong"`), nil
		},
	})
	req := httptest.NewRequest("POST", "/tools/call", strings.NewReader(`{"name":"ping"}`))
	rec := httptest.NewRecorder()
	MCPToolsCallHandler(reg).ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// Guard: the handler struct in mcp.go must be reachable from the wire-facing
// MCPToolEntry shape (tests above rely on json roundtrip). Keep the _ = trick
// to surface unused-field refactors early.
var _ = MCPToolEntry{Name: "", Description: "", InputSchema: nil, OutputSchema: nil}
var _ = MCPResourceEntry{Name: "", Description: "", Schema: nil}

// Ensure handler interface matches registry decl at build time.
var _ func(context.Context, json.RawMessage) (json.RawMessage, error) = registry.MCPToolDecl{}.Handler
var _ func(context.Context) (json.RawMessage, error) = registry.MCPResourceDecl{}.Handler

// Unused import guard for net/http so static-check doesn't trip on our edits.
var _ = http.StatusOK
