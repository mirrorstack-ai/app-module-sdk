package contributions

import (
	"encoding/json"
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
