package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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
// derived at registration via reflection and enforced against incoming JSON at
// call time (NOT statically against the wire format). Enforcement covers both
// constraints the derived schema states: a missing `required` argument and an
// unknown key (additionalProperties:false) are each rejected as ErrInvalidArgs
// rather than silently becoming a zero value.
//
// DOCUMENT YOUR FIELDS. The model picks arguments from the input schema alone,
// so an undescribed field is one it has to guess at. Use the jsonschema struct
// tag — it needs no SDK API and shows up in tools/list immediately:
//
//	type GreetArgs struct {
//	    Name string `json:"name" jsonschema:"description=Who to greet."`
//	}
//
// Registration logs a warning naming every input field that has no
// description.
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
		Handler:      wrapMCPToolHandler(handler, inputSchema),
	}
	if cfg.permission != "" {
		decl.Permission, _ = m.ensurePermissionDeclared("MCPTool", cfg.permission)
	}
	m.warnUndocumentedFields(name, inputSchema)
	m.registry.AddMCPTool(decl)
}

// warnUndocumentedFields logs the input fields a tool ships with no
// description.
//
// 🔴 WHY THIS IS WORTH A LOG LINE. Across the fleet, 0 of 45 shipped tools
// documented a single field. The model chooses arguments from the input schema
// and nothing else, so an undescribed field is one it fills by guessing at the
// name — and `jsonschema:"description=..."` already worked, at zero cost, the
// whole time. Nothing ever told anyone, so nobody did it.
//
// A warning rather than an error: descriptions are a quality bar, and failing
// a module's boot over prose would be a worse trade than shipping it
// undocumented. It names the fields so the fix is mechanical.
func (m *Module) warnUndocumentedFields(tool string, inputSchema json.RawMessage) {
	var parsed struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(inputSchema, &parsed); err != nil {
		return
	}
	undocumented := make([]string, 0, len(parsed.Properties))
	for field, prop := range parsed.Properties {
		if prop.Description == "" {
			undocumented = append(undocumented, field)
		}
	}
	if len(undocumented) == 0 {
		return
	}
	// Map iteration order is random; a log line that reorders between boots is
	// noise in a diff and unusable in a test.
	sort.Strings(undocumented)
	m.logger.Printf("MCPTool(%s): input fields have no description: %s — add `jsonschema:\"description=...\"` struct tags so the agent stops guessing",
		tool, strings.Join(undocumented, ", "))
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

// requiredProperties reads the `required` array out of a derived input schema.
// Parsed ONCE at registration — the result is closed over by the handler, so
// the call path never re-parses the schema.
func requiredProperties(schema json.RawMessage) []string {
	var parsed struct {
		Required []string `json:"required"`
	}
	// A schema that will not parse is not a reason to reject every call: the
	// unmarshal below still enforces types and unknown fields. Derivation
	// failure already panicked at registration, so this is unreachable in
	// practice and fails OPEN only for the required check.
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return nil
	}
	return parsed.Required
}

// wrapMCPToolHandler adapts a typed handler into the type-erased registry form
// and enforces the input schema the module published.
//
// 🔴 THE SCHEMA USED TO BE ADVERTISED AND ENFORCED BY NOBODY. MCPTool's own doc
// comment promised args were "validated against incoming JSON at call time",
// and the only thing that ran was json.Unmarshal — which ignores both of the
// constraints the derived schema states:
//
//   - `required` — Go leaves an absent field at its zero value, so a tool
//     declaring a required userId ran with userId="" and, if that meant "all
//     users" or "me", did the wrong thing silently. An LLM omitting an argument
//     is the ordinary case, not the exotic one.
//   - `additionalProperties:false` — Go drops unknown keys without a word, so a
//     model calling `{"user_id": ...}` against a `userId` field got the same
//     silent zero value plus no signal that it had misspelled anything.
//
// Both are now rejected as ErrInvalidArgs (400), which the MCP layer turns into
// an in-band error the model can read and retry against. A wrong answer
// delivered confidently is worse than an error, and this closes the gap between
// what the schema says and what the module does.
func wrapMCPToolHandler[In, Out any](handler func(context.Context, In) (Out, error), inputSchema json.RawMessage) registry.MCPToolHandler {
	required := requiredProperties(inputSchema)
	return func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
		var in In
		if len(args) > 0 && string(args) != "null" {
			// Presence check first, against the raw object: JSON Schema's
			// `required` is about the KEY being present, so an explicit null
			// satisfies it and a zero value from an absent key does not.
			if len(required) > 0 {
				var present map[string]json.RawMessage
				if err := json.Unmarshal(args, &present); err != nil {
					return nil, fmt.Errorf("%w: %s", system.ErrInvalidArgs, err.Error())
				}
				for _, name := range required {
					if _, ok := present[name]; !ok {
						return nil, fmt.Errorf("%w: missing required argument %q", system.ErrInvalidArgs, name)
					}
				}
			}
			// DisallowUnknownFields IS additionalProperties:false: the schema's
			// properties are derived from In's fields, so "unknown to the
			// struct" and "not in properties" are the same set, at every level
			// of nesting.
			dec := json.NewDecoder(bytes.NewReader(args))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				return nil, fmt.Errorf("%w: %s", system.ErrInvalidArgs, err.Error())
			}
		} else if len(required) > 0 {
			// No args at all, but the schema says some are mandatory. Running
			// the handler on an all-zero In is precisely the silent-wrong-answer
			// case above.
			return nil, fmt.Errorf("%w: missing required argument %q", system.ErrInvalidArgs, required[0])
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
