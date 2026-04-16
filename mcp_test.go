package mirrorstack

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/system"
)

// ---- MCP integration ----

type greetArgs struct {
	Name string `json:"name"`
}

type greetResult struct {
	Message string `json:"message"`
}

func TestMCPTool_RegistersAndInvokesViaRoute(t *testing.T) {
	// No t.Parallel: newTestModuleWithSecret calls t.Setenv, which is incompatible with parallel.
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	MCPTool("greet", "Say hi", func(ctx context.Context, a greetArgs) (greetResult, error) {
		return greetResult{Message: "hi " + a.Name}, nil
	})

	// Call via the mounted route.
	body := strings.NewReader(`{"name":"greet","args":{"name":"world"}}`)
	req := httptest.NewRequest("POST", "/__mirrorstack/mcp/tools/call", body)
	req.Header.Set("X-MS-Internal-Secret", "secret")
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("tools/call status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Result greetResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Result.Message != "hi world" {
		t.Errorf("result.message = %q, want %q", resp.Result.Message, "hi world")
	}
}

func TestMCPTool_ToolsListRequiresInternalAuth(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	MCPTool("greet", "Say hi", func(ctx context.Context, a greetArgs) (greetResult, error) {
		return greetResult{Message: "hi"}, nil
	})

	// No secret header.
	req := httptest.NewRequest("GET", "/__mirrorstack/mcp/tools/list", nil)
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)
	if rec.Code == 200 {
		t.Errorf("tools/list without auth returned 200, want 401/403")
	}
}

func TestMCPTool_SchemasDerivedFromStructs(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	MCPTool("greet", "Say hi", func(ctx context.Context, a greetArgs) (greetResult, error) {
		return greetResult{}, nil
	})

	tool, ok := m.registry.MCPTool("greet")
	if !ok {
		t.Fatal("tool not registered")
	}
	// The JSON Schema must describe the args struct's fields.
	if !strings.Contains(string(tool.InputSchema), `"name"`) {
		t.Errorf("InputSchema missing 'name' field: %s", tool.InputSchema)
	}
	if !strings.Contains(string(tool.OutputSchema), `"message"`) {
		t.Errorf("OutputSchema missing 'message' field: %s", tool.OutputSchema)
	}
}

func TestMCPResource_RegistersAndReadsViaRoute(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	type statusOut struct {
		Healthy bool `json:"healthy"`
	}
	MCPResource("status", "Module status", func(ctx context.Context) (statusOut, error) {
		return statusOut{Healthy: true}, nil
	})

	req := httptest.NewRequest("GET", "/__mirrorstack/mcp/resources/read?name=status", nil)
	req.Header.Set("X-MS-Internal-Secret", "secret")
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("resources/read status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"healthy":true`) {
		t.Errorf("body = %s, want content with healthy:true", rec.Body.String())
	}
}

func TestMCPTool_ManifestIncludesMCPSurface(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	MCPTool("greet", "Say hi", func(ctx context.Context, a greetArgs) (greetResult, error) {
		return greetResult{}, nil
	})

	req := httptest.NewRequest("GET", "/__mirrorstack/platform/manifest", nil)
	req.Header.Set("X-MS-Internal-Secret", "secret")
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)

	var payload system.ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.MCP.Tools) != 1 || payload.MCP.Tools[0].Name != "greet" {
		t.Errorf("manifest.mcp.tools = %+v, want [greet]", payload.MCP.Tools)
	}
}
