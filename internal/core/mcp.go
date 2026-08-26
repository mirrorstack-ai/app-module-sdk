package core

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
	"github.com/mirrorstack-ai/app-module-sdk/system"
)

// MCPToolOption configures an MCPTool declaration. The variadic seam carries
// future per-tool knobs (e.g. risk tier) without breaking existing callers.
type MCPToolOption func(*mcpToolConfig)

type mcpToolConfig struct {
	permission string // SHORT permission name; slug-qualified at registration
}

// ToolPermission gates the tool on a module permission, looked up by SHORT
// name and slug-qualified exactly like RegisterPermission. The platform lists
// and invokes the tool only for callers whose effective permissions include
// it.
//
// Safe-by-default: if the name was never RegisterPermission'd, it is
// registered lazily as ADMIN-ONLY (a dev warning is logged) so a typo locks
// the tool down rather than opening it — same rule as RequirePermission.
func ToolPermission(name string) MCPToolOption {
	return func(c *mcpToolConfig) { c.permission = name }
}

// MCPTool registers an agent-callable tool on the default module. Input and
// output JSON Schemas are derived from the In and Out type parameters via
// reflection; struct fields use their `json:"..."` tags. The handler receives
// parsed typed args and returns a typed result.
//
// Name must satisfy registry.ValidateName (no path separators, whitespace, or
// null bytes). First-wins: a duplicate registration is a no-op (including its
// options).
//
// The tool is served at POST /__mirrorstack/mcp/tools/call under Internal
// scope. The platform aggregates tools from all installed modules into a
// single agent-facing MCP server.
//
//	type GreetArgs struct{ Name string `json:"name"` }
//	type GreetResult struct{ Message string `json:"message"` }
//	ms.MCPTool("greet", "Say hi to someone",
//	    func(ctx context.Context, a GreetArgs) (GreetResult, error) {
//	        return GreetResult{Message: "hi " + a.Name}, nil
//	    })
//
// Generics give compile-time type safety on the handler signature; schemas are
// derived at registration via reflection and validated against incoming JSON
// at call time (NOT statically against the wire format).
//
// Optional MCPToolOptions scope the tool, e.g. ms.ToolPermission("users.read").
//
// Panics before Init or on schema derivation failure.
func MCPTool[In, Out any](name, description string, handler func(ctx context.Context, args In) (Out, error), opts ...MCPToolOption) {
	m := mustDefault("MCPTool")
	inputSchema, err := deriveMCPSchema[In]()
	if err != nil {
		panic("mirrorstack: MCPTool(" + name + ") input schema derivation failed: " + err.Error())
	}
	outputSchema, err := deriveMCPSchema[Out]()
	if err != nil {
		panic("mirrorstack: MCPTool(" + name + ") output schema derivation failed: " + err.Error())
	}
	var cfg mcpToolConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	decl := registry.MCPToolDecl{
		Name:         name,
		Description:  description,
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
		Handler:      wrapMCPToolHandler(handler),
	}
	if cfg.permission != "" {
		decl.Permission, _ = m.ensurePermissionDeclared("MCPTool", cfg.permission)
	}
	m.registry.AddMCPTool(decl)
}

// MCPResource registers an agent-readable resource on the default module. The
// handler returns current content on demand. Output schema is derived from Out.
// Panics before Init or on schema derivation failure.
//
// Deprecated: nothing can reach an MCP resource. The dispatch MCP server
// answers `method not found` to every resources/* call — its v1 capability set
// is tools only, by design (api-platform, MCPServerHandler's JSON-RPC switch),
// and the SDK never advertises a resources capability either. A registered
// resource is therefore served on the module's own router and read by nobody:
// adoption across the fleet is 0 of 11 modules, which is the expected outcome
// for a surface with no client.
//
// Expose the same data as an MCPTool with no arguments. A tool is reachable,
// it carries the same derived output schema, and it can be annotated read-only
// (see the Tool* options) so an agent treats it exactly as it would a resource.
//
// This will be removed once the last caller is gone. It is not removed now
// because doing so is a breaking change to a published module API, and there
// is no evidence anyone is calling it — only evidence that it would not work
// if they did.
func MCPResource[Out any](name, description string, handler func(ctx context.Context) (Out, error)) {
	m := mustDefault("MCPResource")
	schema, err := deriveMCPSchema[Out]()
	if err != nil {
		panic("mirrorstack: MCPResource(" + name + ") schema derivation failed: " + err.Error())
	}
	m.registry.AddMCPResource(registry.MCPResourceDecl{
		Name:        name,
		Description: description,
		Schema:      schema,
		Handler:     wrapMCPResourceHandler(handler),
	})
}

// wrapMCPToolHandler adapts a typed handler into the type-erased registry form.
// Args unmarshal failures become ErrInvalidArgs (400); handler errors pass
// through (default to 500 unless the handler returned ErrInvalidArgs itself).
func wrapMCPToolHandler[In, Out any](handler func(context.Context, In) (Out, error)) registry.MCPToolHandler {
	return func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
		var in In
		if len(args) > 0 && string(args) != "null" {
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("%w: %s", system.ErrInvalidArgs, err.Error())
			}
		}
		out, err := handler(ctx, in)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
}

func wrapMCPResourceHandler[Out any](handler func(context.Context) (Out, error)) registry.MCPResourceHandler {
	return func(ctx context.Context) (json.RawMessage, error) {
		out, err := handler(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
}
