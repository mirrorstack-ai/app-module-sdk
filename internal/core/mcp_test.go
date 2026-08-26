package core

import (
	"context"
	"encoding/json"
	"errors"
	"log"
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

// The two schema constraints Go's json.Unmarshal ignores must actually be
// enforced, because MCPTool's doc comment has always claimed they are.
//
// Before this, a tool declaring a required userId ran with userId="" when the
// model omitted it, and a model that sent {"user_id":…} against a `userId`
// field got the same silent zero plus no hint it had misspelled anything. A
// wrong answer delivered confidently is worse than an error.
func TestMCPTool_EnforcesRequiredAndUnknownArgs(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	type args struct {
		Name  string `json:"name"`
		Limit int    `json:"limit,omitempty"`
	}
	called := false
	handler := wrapMCPToolHandler(
		func(_ context.Context, a args) (greetResult, error) {
			called = true
			return greetResult{Message: a.Name}, nil
		},
		mustSchema[args](t),
	)

	cases := []struct {
		name    string
		args    string
		wantErr bool
		reason  string
	}{
		{"present", `{"name":"ada"}`, false, "the ordinary call must still work"},
		{"optional omitted is fine", `{"name":"ada"}`, false, "limit has omitempty, so it is not required"},
		{"explicit null satisfies presence", `{"name":null}`, false, "JSON Schema required is about the KEY, not the value"},
		{"required missing", `{"limit":5}`, true, "name is required and absent"},
		{"no args at all", `{}`, true, "an empty object is still missing name"},
		{"null args", `null`, true, "no arguments at all, but name is required"},
		{"unknown key", `{"name":"ada","user_id":"x"}`, true, "additionalProperties:false is advertised"},
		{"misspelled required", `{"nmae":"ada"}`, true, "must not read as an absent name plus an unknown key silently"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			_, err := handler(context.Background(), json.RawMessage(tc.args))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("args %s: no error — %s", tc.args, tc.reason)
				}
				if !errors.Is(err, system.ErrInvalidArgs) {
					t.Errorf("args %s: err = %v, want ErrInvalidArgs (a 400, not a 500)", tc.args, err)
				}
				if called {
					t.Error("the handler ran on arguments the schema rejects")
				}
				return
			}
			if err != nil {
				t.Fatalf("args %s: unexpected error %v — %s", tc.args, err, tc.reason)
			}
			if !called {
				t.Error("the handler did not run on valid arguments")
			}
		})
	}
}

// Registration must name the input fields a tool ships undocumented. The model
// picks arguments from the input schema alone, and 0 of 45 shipped tools
// documented a single field — because the jsonschema tag worked all along and
// nothing ever said so.
func TestMCPTool_WarnsAboutUndocumentedFields(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	var logged strings.Builder
	m.logger = log.New(&logged, "", 0)

	type documented struct {
		Name string `json:"name" jsonschema:"description=Who to greet."`
	}
	type partial struct {
		Name  string `json:"name" jsonschema:"description=Who to greet."`
		Limit int    `json:"limit"`
		Zzz   string `json:"zzz"`
	}

	MCPTool("documented", "d", func(_ context.Context, a documented) (greetResult, error) {
		return greetResult{}, nil
	})
	if logged.Len() != 0 {
		t.Errorf("a fully documented tool warned anyway: %s", logged.String())
	}

	MCPTool("partial", "p", func(_ context.Context, a partial) (greetResult, error) {
		return greetResult{}, nil
	})
	out := logged.String()
	if !strings.Contains(out, "MCPTool(partial)") {
		t.Errorf("warning does not name the tool: %s", out)
	}
	// Sorted, so the line is stable across boots and readable in a diff.
	if !strings.Contains(out, "limit, zzz") {
		t.Errorf("warning does not name the undocumented fields in sorted order: %s", out)
	}
	if strings.Contains(out, "name") {
		t.Errorf("warning names a field that IS documented: %s", out)
	}
}

func mustSchema[T any](t *testing.T) json.RawMessage {
	t.Helper()
	schema, err := deriveMCPSchema[T]()
	if err != nil {
		t.Fatal(err)
	}
	return schema
}
