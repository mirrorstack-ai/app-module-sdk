package contributions

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRegistryListCopiesPayloadSchema(t *testing.T) {
	t.Parallel()

	// want is a string, not the json.RawMessage handed to Define: comparing
	// against the slice the caller still holds would alias whatever the
	// registry stored, so an uncloned List() would mutate both sides and the
	// assertion would hold vacuously.
	const want = `{"type":"string"}`

	r := NewRegistry()
	if err := r.Define(NewSlot("profile", "string", json.RawMessage(want), nil)); err != nil {
		t.Fatal(err)
	}

	got := r.List()
	if len(got) != 1 || string(got[0].Payload) != want {
		t.Fatalf("List() = %#v, want payload %s", got, want)
	}
	got[0].Payload[0] = '['

	again := r.List()
	if string(again[0].Payload) != want {
		t.Fatalf("mutating List payload corrupted registry: got %s, want %s", again[0].Payload, want)
	}
}

func TestContributionDecodeIsStrict(t *testing.T) {
	t.Parallel()

	type payload struct {
		Name string `json:"name"`
	}
	for name, raw := range map[string]string{
		"unknown field": `{"name":"Ada","admin":true}`,
		"second value":  `{"name":"Ada"} {"name":"Grace"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var got payload
			if err := (Contribution{Payload: json.RawMessage(raw)}).Decode(&got); err == nil {
				t.Fatal("Decode accepted a non-contract payload")
			}
		})
	}

	var got payload
	if err := (Contribution{Payload: json.RawMessage(`{"name":"Ada"}`)}).Decode(&got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if strings.TrimSpace(got.Name) != "Ada" {
		t.Fatalf("name = %q, want Ada", got.Name)
	}
}
