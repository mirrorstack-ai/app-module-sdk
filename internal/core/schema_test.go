package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/internal/contributions"
)

type schemaNested struct {
	Value string `json:"value"`
}

type schemaContainer struct {
	Renamed  string                  `json:"renamed"`
	Optional string                  `json:"optional,omitempty"`
	Ignored  string                  `json:"-"`
	Nested   schemaNested            `json:"nested"`
	Slice    []schemaNested          `json:"slice"`
	Map      map[string]schemaNested `json:"map"`
	Pointer  *schemaNested           `json:"pointer"`
}

type cyclicNode struct {
	Name string       `json:"name"`
	Next *cyclicNode  `json:"next"`
	Kids []cyclicNode `json:"kids"`
}

func deriveSchemaDocument[T any](t *testing.T) (json.RawMessage, map[string]any) {
	t.Helper()
	raw, err := derivePayloadSchema[T]()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return raw, doc
}

func schemaDefinition(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	defs, ok := doc["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("$defs = %#v, want object", doc["$defs"])
	}
	def, ok := defs[name].(map[string]any)
	if !ok {
		t.Fatalf("$defs[%q] = %#v, want object", name, defs[name])
	}
	return def
}

func TestDerivePayloadSchemaHonorsJSONShape(t *testing.T) {
	t.Parallel()

	_, doc := deriveSchemaDocument[schemaContainer](t)
	root := schemaDefinition(t, doc, "schemaContainer")
	properties := root["properties"].(map[string]any)
	if _, ok := properties["renamed"]; !ok {
		t.Fatal("renamed JSON field is absent")
	}
	if _, ok := properties["Ignored"]; ok {
		t.Fatal(`json:"-" field appears in properties`)
	}
	if _, ok := properties["ignored"]; ok {
		t.Fatal(`json:"-" field appears in properties`)
	}
	required := root["required"].([]any)
	for _, name := range required {
		if name == "optional" {
			t.Fatal("omitempty field appears in required")
		}
	}
}

func TestDerivePayloadSchemaReferencesResolveAndCollectionsKeepElementShape(t *testing.T) {
	t.Parallel()

	_, doc := deriveSchemaDocument[schemaContainer](t)
	defs := doc["$defs"].(map[string]any)
	var walk func(any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if ref, ok := value["$ref"].(string); ok && strings.HasPrefix(ref, "#/$defs/") {
				name := strings.TrimPrefix(ref, "#/$defs/")
				if _, ok := defs[name]; !ok {
					t.Errorf("dangling $ref %q", ref)
				}
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(doc)

	root := schemaDefinition(t, doc, "schemaContainer")
	properties := root["properties"].(map[string]any)
	if properties["nested"].(map[string]any)["$ref"] != "#/$defs/schemaNested" {
		t.Fatalf("nested schema = %#v", properties["nested"])
	}
	if properties["slice"].(map[string]any)["items"].(map[string]any)["$ref"] != "#/$defs/schemaNested" {
		t.Fatalf("slice schema = %#v", properties["slice"])
	}
	if properties["map"].(map[string]any)["additionalProperties"].(map[string]any)["$ref"] != "#/$defs/schemaNested" {
		t.Fatalf("map schema = %#v", properties["map"])
	}
	if properties["pointer"].(map[string]any)["$ref"] != "#/$defs/schemaNested" {
		t.Fatalf("pointer schema = %#v", properties["pointer"])
	}
}

func TestDerivePayloadSchemaScalars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(*testing.T) map[string]any
		want string
	}{
		{"string", func(t *testing.T) map[string]any { _, d := deriveSchemaDocument[string](t); return d }, "string"},
		{"bool", func(t *testing.T) map[string]any { _, d := deriveSchemaDocument[bool](t); return d }, "boolean"},
		{"int", func(t *testing.T) map[string]any { _, d := deriveSchemaDocument[int](t); return d }, "integer"},
		{"int8", func(t *testing.T) map[string]any { _, d := deriveSchemaDocument[int8](t); return d }, "integer"},
		{"int16", func(t *testing.T) map[string]any { _, d := deriveSchemaDocument[int16](t); return d }, "integer"},
		{"int32", func(t *testing.T) map[string]any { _, d := deriveSchemaDocument[int32](t); return d }, "integer"},
		{"int64", func(t *testing.T) map[string]any { _, d := deriveSchemaDocument[int64](t); return d }, "integer"},
		{"uint", func(t *testing.T) map[string]any { _, d := deriveSchemaDocument[uint](t); return d }, "integer"},
		{"uint8", func(t *testing.T) map[string]any { _, d := deriveSchemaDocument[uint8](t); return d }, "integer"},
		{"uint16", func(t *testing.T) map[string]any { _, d := deriveSchemaDocument[uint16](t); return d }, "integer"},
		{"uint32", func(t *testing.T) map[string]any { _, d := deriveSchemaDocument[uint32](t); return d }, "integer"},
		{"uint64", func(t *testing.T) map[string]any { _, d := deriveSchemaDocument[uint64](t); return d }, "integer"},
		{"float32", func(t *testing.T) map[string]any { _, d := deriveSchemaDocument[float32](t); return d }, "number"},
		{"float64", func(t *testing.T) map[string]any { _, d := deriveSchemaDocument[float64](t); return d }, "number"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.run(t)["type"]; got != tc.want {
				t.Fatalf("type = %v, want %q", got, tc.want)
			}
		})
	}
}

func TestDerivePayloadSchemaSpecialTypes(t *testing.T) {
	t.Parallel()

	_, timeDoc := deriveSchemaDocument[time.Time](t)
	if timeDoc["type"] != "string" || timeDoc["format"] != "date-time" {
		t.Fatalf("time.Time schema = %#v", timeDoc)
	}
	type withOpenValues struct {
		Raw       json.RawMessage `json:"raw"`
		Interface interface{}     `json:"interface"`
	}
	_, openDoc := deriveSchemaDocument[withOpenValues](t)
	properties := schemaDefinition(t, openDoc, "withOpenValues")["properties"].(map[string]any)
	for _, name := range []string{"raw", "interface"} {
		if properties[name] != true {
			t.Fatalf("%s field schema = %#v, want true", name, properties[name])
		}
	}
}

func TestDerivePayloadSchemaCycleTerminates(t *testing.T) {
	t.Parallel()

	// If this test fails by stack overflow, every module declaring such a
	// payload type dies at boot; that is why payload schemas retain $defs.
	raw, doc := deriveSchemaDocument[cyclicNode](t)
	if len(raw) == 0 {
		t.Fatal("derived schema is empty")
	}
	schemaDefinition(t, doc, "cyclicNode")
}

func TestDerivePayloadSchemaDeterministic(t *testing.T) {
	t.Parallel()

	want, err := derivePayloadSchema[schemaContainer]()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		got, err := derivePayloadSchema[schemaContainer]()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("derivation %d differs:\ngot  %s\nwant %s", i, got, want)
		}
	}
}

// The derivation is only worth anything if Provide[T] actually carries it onto
// the slot's manifest projection — the declared-but-never-wired failure this
// whole change exists to make impossible.
func TestNewContributionSlotProjectsPayloadSchema(t *testing.T) {
	t.Parallel()

	r := contributions.NewRegistry()
	if err := r.Define(NewContributionSlot[schemaContainer]("user-detail-blocks")); err != nil {
		t.Fatal(err)
	}

	got := r.List()
	if len(got) != 1 {
		t.Fatalf("List() length = %d, want 1", len(got))
	}
	want, err := derivePayloadSchema[schemaContainer]()
	if err != nil {
		t.Fatal(err)
	}
	if string(got[0].Payload) != string(want) {
		t.Fatalf("slot payload schema = %s, want %s", got[0].Payload, want)
	}
}

func TestDerivePayloadSchemaAnyDoesNotPanic(t *testing.T) {
	t.Parallel()

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("derivePayloadSchema[any]() panicked: %v", recovered)
		}
	}()
	raw, err := derivePayloadSchema[any]()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("derived schema is empty")
	}
}

// deriveMCPSchema must not leak the declaring package's import path, and must
// not carry dialect metadata no MCP client reads.
//
// Both cost tokens on EVERY tools/list, and a listing is re-sent every turn —
// so this is paid per turn, per tool, for the life of the conversation. The
// $id also published the SDK's internal package layout to third-party clients.
// derivePayloadSchema had suppressed $id since it was written; this path simply
// never got the same treatment, which is why the assertion is here rather than
// in a comment.
func TestDeriveMCPSchemaOmitsIDAndDialect(t *testing.T) {
	t.Parallel()

	type args struct {
		UserID string `json:"userId"`
	}
	raw, err := deriveMCPSchema[args]()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("derived schema is not an object: %v", err)
	}
	for _, key := range []string{"$id", "$schema"} {
		if v, present := got[key]; present {
			t.Errorf("derived MCP schema carries %s = %v; it must not", key, v)
		}
	}
	// The MCP spec requires inputSchema to be an object schema. Dropping the
	// two keys above must not cost us that — a top-level $ref or a missing
	// type makes clients reject the whole listing.
	if got["type"] != "object" {
		t.Errorf(`type = %v, want "object" (got %s)`, got["type"], raw)
	}
	if _, present := got["$ref"]; present {
		t.Errorf("derived MCP schema is a $ref, not an inline object: %s", raw)
	}
}
