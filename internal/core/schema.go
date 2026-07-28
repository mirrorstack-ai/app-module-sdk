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
	r := &jsonschema.Reflector{DoNotReference: true}
	return json.Marshal(r.Reflect(zero))
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
