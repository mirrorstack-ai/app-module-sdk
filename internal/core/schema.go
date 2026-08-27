package core

import (
	"encoding/json"
	"reflect"

	"github.com/invopop/jsonschema"
)

// deriveMCPSchema returns the JSON Schema of T for an MCP tool/resource
// declaration. Its reflector config deliberately differs from
// derivePayloadSchema's — the two are not interchangeable, see there.
func deriveMCPSchema[T any]() (json.RawMessage, error) {
	var zero T
	// DoNotReference inlines the struct schema so the top level is a concrete
	// {"type":"object",...} rather than invopop's default {"$ref":"#/$defs/..."}.
	// The MCP spec requires inputSchema/outputSchema to be an object schema with
	// type:"object"; a top-level $ref makes clients reject the whole tools/list.
	//
	// Anonymous suppresses the $id invopop derives from the Go import path. Left
	// on, every tool's inputSchema carried a line like
	//
	//	"$id":"https://github.com/mirrorstack-ai/app-module-sdk/internal/core/args"
	//
	// which leaks the declaring package's layout to every MCP client and costs
	// tokens on every tools/list — the listing is re-sent each turn, so it is
	// paid per turn, per tool, forever. derivePayloadSchema already set this;
	// the two paths simply never got the same treatment.
	r := &jsonschema.Reflector{DoNotReference: true, Anonymous: true}
	schema := r.Reflect(zero)
	// $schema is dialect metadata an MCP client never reads — it consumes
	// inputSchema as a JSON Schema object and looks at type/properties/required.
	// Another ~55 bytes per tool per listing, for nothing.
	schema.Version = ""
	return json.Marshal(schema)
}

// derivePayloadSchema returns the full recursive JSON Schema of a contribution
// slot's payload type T, for the manifest to carry to contributors who cannot
// read the host's source.
//
// It keeps DoNotReference at its false default — unlike deriveMCPSchema, which
// must inline. Inlining a self-referential payload type recurses until the
// module STACK-OVERFLOWS at boot; it crashes the process rather than returning
// an error, so this is a crash-safety choice, not a formatting one. With $defs
// the cycle terminates at a {"$ref":"#/$defs/T"}. ExpandedStruct is not the way
// back to an inline top level either: it lifts the root out of $defs while
// nested self-references still point at it, dangling the $ref for exactly the
// cyclic case this config exists to survive.
//
// Anonymous suppresses the $id invopop otherwise derives from the Go import
// path, which would leak the host's package layout into the manifest and churn
// whenever a package moves. ReflectFromType carries T's static type; Reflect
// takes an any, so T = any (or any interface) arrives as a nil interface and
// invopop nil-dereferences.
func derivePayloadSchema[T any]() (json.RawMessage, error) {
	r := &jsonschema.Reflector{Anonymous: true}
	return json.Marshal(r.ReflectFromType(reflect.TypeFor[T]()))
}
