package system

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// MCPToolsListHandler returns the handler for GET /__mirrorstack/mcp/tools/list.
// Emits the full tool surface (name, description, input/output schemas) so the
// platform's aggregated MCP server can route agent calls without live-listing
// per module. The list mirrors ManifestPayload.MCP.Tools — either suffices.
func MCPToolsListHandler(reg *registry.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httputil.JSON(w, http.StatusOK, mcpToolsListResponse{Tools: toolEntries(reg.MCPTools())})
	}
}

// toolEntries projects registry decls into wire entries, stripping Handler
// (not JSON-serializable). Shared by list handlers and buildManifestMCP.
func toolEntries(decls []registry.MCPToolDecl) []MCPToolEntry {
	out := make([]MCPToolEntry, len(decls))
	for i, t := range decls {
		out[i] = MCPToolEntry{
			Name: t.Name, Description: t.Description,
			InputSchema: t.InputSchema, OutputSchema: t.OutputSchema,
		}
	}
	return out
}

// resourceEntries is the resource counterpart to toolEntries.
func resourceEntries(decls []registry.MCPResourceDecl) []MCPResourceEntry {
	out := make([]MCPResourceEntry, len(decls))
	for i, rc := range decls {
		out[i] = MCPResourceEntry{
			Name: rc.Name, Description: rc.Description, Schema: rc.Schema,
		}
	}
	return out
}

// MCPToolsCallHandler invokes a registered tool by name with the given args.
// Body: {"name": "...", "args": {...}}. Returns {"result": ...} on success.
// 404 for unknown name, 400 for invalid body or args that fail the tool's
// handler, 500 for handler errors.
func MCPToolsCallHandler(reg *registry.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req mcpToolCallRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid request body: " + err.Error()})
			return
		}
		if req.Name == "" {
			httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "missing tool name"})
			return
		}
		tool, ok := reg.MCPTool(req.Name)
		if !ok {
			httputil.JSON(w, http.StatusNotFound, httputil.ErrorResponse{Error: "tool not found: " + req.Name})
			return
		}
		if tool.Handler == nil {
			httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: "tool has no handler: " + req.Name})
			return
		}
		args := req.Args
		if len(args) == 0 {
			args = json.RawMessage("null")
		}
		result, err := tool.Handler(r.Context(), args)
		if err != nil {
			if errors.Is(err, ErrInvalidArgs) {
				httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
				return
			}
			httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
			return
		}
		httputil.JSON(w, http.StatusOK, mcpToolCallResponse{Result: result})
	}
}

// MCPResourcesListHandler returns the handler for GET /__mirrorstack/mcp/resources/list.
func MCPResourcesListHandler(reg *registry.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httputil.JSON(w, http.StatusOK, mcpResourcesListResponse{Resources: resourceEntries(reg.MCPResources())})
	}
}

// MCPResourcesReadHandler returns the handler for GET /__mirrorstack/mcp/resources/read?name=...
func MCPResourcesReadHandler(reg *registry.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "missing name parameter"})
			return
		}
		rc, ok := reg.MCPResource(name)
		if !ok {
			httputil.JSON(w, http.StatusNotFound, httputil.ErrorResponse{Error: "resource not found: " + name})
			return
		}
		if rc.Handler == nil {
			httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: "resource has no handler: " + name})
			return
		}
		content, err := rc.Handler(r.Context())
		if err != nil {
			httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
			return
		}
		httputil.JSON(w, http.StatusOK, mcpResourceReadResponse{Content: content})
	}
}

// ErrInvalidArgs signals a 400-class error from an MCP tool handler (bad input
// shape). Other errors are treated as 500.
var ErrInvalidArgs = errors.New("mcp: invalid args")

// MCPToolEntry is the JSON wire shape for a tool in list responses and the
// manifest. Omits the Handler field from registry.MCPToolDecl.
type MCPToolEntry struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

// MCPResourceEntry is the JSON wire shape for a resource.
type MCPResourceEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

type mcpToolsListResponse struct {
	Tools []MCPToolEntry `json:"tools"`
}

type mcpToolCallRequest struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type mcpToolCallResponse struct {
	Result json.RawMessage `json:"result"`
}

type mcpResourcesListResponse struct {
	Resources []MCPResourceEntry `json:"resources"`
}

type mcpResourceReadResponse struct {
	Content json.RawMessage `json:"content"`
}
