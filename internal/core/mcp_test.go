package core

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/roles"
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

// newSluggedTestModule is newTestModuleWithSecret with a slug, for tests that
// exercise permission qualification ("<slug>.<name>").
func newSluggedTestModule(t *testing.T, id, slug string) *Module {
	t.Helper()
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	m, err := New(Config{ID: id, Slug: slug})
	if err != nil {
		t.Fatalf("New(%q): %v", id, err)
	}
	return m
}

func TestMCPTool_ToolPermissionQualifiesAndProjects(t *testing.T) {
	resetDefault(t)
	m := newSluggedTestModule(t, "demo", "demo")
	defaultModule = m

	RegisterPermission("users.read", PermissionOpts{DefaultRole: roles.Viewer()})
	MCPTool("list-users", "List users", func(ctx context.Context, a greetArgs) (greetResult, error) {
		return greetResult{}, nil
	}, ToolPermission("users.read"))

	tool, ok := m.registry.MCPTool("list-users")
	if !ok {
		t.Fatal("tool not registered")
	}
	if tool.Permission != "demo.users.read" {
		t.Errorf("decl.Permission = %q, want %q (slug-qualified)", tool.Permission, "demo.users.read")
	}

	// Manifest projection carries the qualified name.
	payload := fetchManifest(t, m)
	if len(payload.MCP.Tools) != 1 || payload.MCP.Tools[0].Permission != "demo.users.read" {
		t.Errorf("manifest.mcp.tools = %+v, want permission demo.users.read", payload.MCP.Tools)
	}
	// The declared role set survives — ToolPermission must not clobber the
	// RegisterPermission'd roles with the lazy admin-only fallback.
	var declaredRoles []string
	for _, p := range payload.Permissions {
		if p.Name == "demo.users.read" {
			declaredRoles = p.Roles
		}
	}
	if !slices.Contains(declaredRoles, roles.Viewer().Key) {
		t.Errorf("manifest.permissions[demo.users.read].roles = %v, want viewer preserved", declaredRoles)
	}

	// tools/list wire stays in lockstep with the manifest.
	req := httptest.NewRequest("GET", "/__mirrorstack/mcp/tools/list", nil)
	req.Header.Set("X-MS-Internal-Secret", "secret")
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"permission":"demo.users.read"`) {
		t.Errorf("tools/list body = %s, want permission field", rec.Body.String())
	}
}

func TestMCPTool_NoPermissionOmitsField(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	MCPTool("greet", "Say hi", func(ctx context.Context, a greetArgs) (greetResult, error) {
		return greetResult{}, nil
	})

	// omitempty: the tool entry must not carry a "permission" key at all.
	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	var raw struct {
		MCP struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw manifest: %v", err)
	}
	if len(raw.MCP.Tools) != 1 {
		t.Fatalf("mcp.tools = %+v, want 1 entry", raw.MCP.Tools)
	}
	if _, present := raw.MCP.Tools[0]["permission"]; present {
		t.Errorf("tool entry carries a permission key without ToolPermission: %v", raw.MCP.Tools[0])
	}
}

func TestMCPTool_UndeclaredPermissionRegistersAdminOnly(t *testing.T) {
	resetDefault(t)
	m := newSluggedTestModule(t, "demo", "demo")
	defaultModule = m

	// No RegisterPermission — a typo'd name must fail CLOSED (admin-only),
	// never open, and still land in manifest.permissions so the platform's
	// roles projection resolves.
	MCPTool("nuke", "Dangerous", func(ctx context.Context, a greetArgs) (greetResult, error) {
		return greetResult{}, nil
	}, ToolPermission("ghost.perm"))

	tool, _ := m.registry.MCPTool("nuke")
	if tool.Permission != "demo.ghost.perm" {
		t.Errorf("decl.Permission = %q, want %q", tool.Permission, "demo.ghost.perm")
	}
	payload := fetchManifest(t, m)
	found := false
	for _, p := range payload.Permissions {
		if p.Name == "demo.ghost.perm" {
			found = true
			if len(p.Roles) != 1 || p.Roles[0] != roles.Admin().Key {
				t.Errorf("lazy permission roles = %v, want [admin]", p.Roles)
			}
			if p.DefaultRole != roles.Admin().Key {
				t.Errorf("lazy permission default role = %q, want admin", p.DefaultRole)
			}
		}
	}
	if !found {
		t.Error("manifest.permissions missing demo.ghost.perm (lazy registration did not fire)")
	}
}

func TestMCPTool_DuplicateKeepsFirstPermission(t *testing.T) {
	resetDefault(t)
	m := newSluggedTestModule(t, "demo", "demo")
	defaultModule = m

	RegisterPermission("a", PermissionOpts{DefaultRole: roles.Viewer()})
	RegisterPermission("b", PermissionOpts{DefaultRole: roles.Viewer()})
	handler := func(ctx context.Context, a greetArgs) (greetResult, error) {
		return greetResult{}, nil
	}
	MCPTool("greet", "first", handler, ToolPermission("a"))
	MCPTool("greet", "second", handler, ToolPermission("b"))

	tool, _ := m.registry.MCPTool("greet")
	if tool.Permission != "demo.a" {
		t.Errorf("decl.Permission = %q, want first-wins demo.a", tool.Permission)
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
