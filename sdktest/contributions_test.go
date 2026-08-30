package sdktest_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/internal/contributions"
	"github.com/mirrorstack-ai/app-module-sdk/sdktest"
	"github.com/mirrorstack-ai/app-module-sdk/system"
)

func TestValidateContributionPayloadUsesHostSchema(t *testing.T) {
	t.Parallel()

	host := system.ManifestPayload{Provides: []contributions.SlotInfo{{
		Key: "cards",
		Payload: json.RawMessage(`{
			"type":"object",
			"properties":{"component":{"type":"string"}},
			"required":["component"],
			"additionalProperties":false
		}`),
	}}}

	valid := sdktest.OutboundContribution{Slot: "cards", Payload: json.RawMessage(`{"component":"Profile"}`)}
	if err := sdktest.ValidateContributionPayload(host, valid); err != nil {
		t.Fatalf("valid contribution: %v", err)
	}
	for _, payload := range []string{`{}`, `{"component":"Profile","href":"/profile"}`, `null`} {
		outbound := sdktest.OutboundContribution{Slot: "cards", Payload: json.RawMessage(payload)}
		if err := sdktest.ValidateContributionPayload(host, outbound); err == nil {
			t.Fatalf("invalid contribution %s was accepted", payload)
		}
	}

	missing := sdktest.OutboundContribution{Slot: "unknown", Payload: json.RawMessage(`{}`)}
	if err := sdktest.ValidateContributionPayload(host, missing); !errors.Is(err, sdktest.ErrContributionSlotNotProvided) {
		t.Fatalf("missing slot error = %v", err)
	}
}

func TestValidateContributionsToHostFiltersByHostIdentity(t *testing.T) {
	t.Parallel()

	var host system.ManifestPayload
	if err := json.Unmarshal([]byte(`{
		"id":"host-id",
		"slug":"user-core",
		"provides":[{
			"key":"cards",
			"payload":{"type":"object","properties":{"component":{"type":"string"}},"required":["component"],"additionalProperties":false}
		}]
	}`), &host); err != nil {
		t.Fatal(err)
	}
	var contributor system.ManifestPayload
	if err := json.Unmarshal([]byte(`{
		"slug":"profile",
		"contributesTo":[
			{"host":"other","slot":"ignored","payload":{}},
			{"host":"user-core","slot":"cards","payload":{"component":"Profile"}}
		]
	}`), &contributor); err != nil {
		t.Fatal(err)
	}
	if err := sdktest.ValidateContributionsToHost(host, contributor); err != nil {
		t.Fatalf("compatibility = %v", err)
	}

	contributor.ContributesTo = contributor.ContributesTo[:1]
	if err := sdktest.ValidateContributionsToHost(host, contributor); !errors.Is(err, sdktest.ErrNoContributionsToHost) {
		t.Fatalf("no-match error = %v", err)
	}
}

func TestValidateContributionsToHostAcceptsExplicitCatalogReference(t *testing.T) {
	t.Parallel()

	host := system.ManifestPayload{
		ID:   "host-id",
		Slug: "user-core",
		Provides: []contributions.SlotInfo{{
			Key: "cards",
			Payload: json.RawMessage(`{
				"type":"object",
				"properties":{"component":{"type":"string"}},
				"required":["component"],
				"additionalProperties":false
			}`),
		}},
	}
	contributor := system.ManifestPayload{
		Slug: "profile",
		ContributesTo: []sdktest.OutboundContribution{{
			Host:    "@mirrorstack/user-core",
			Slot:    "cards",
			Payload: json.RawMessage(`{"component":"Profile"}`),
		}},
	}

	if err := sdktest.ValidateContributionsToHost(host, contributor); !errors.Is(err, sdktest.ErrNoContributionsToHost) {
		t.Fatalf("owner-qualified reference matched by slug alone: %v", err)
	}
	if err := sdktest.ValidateContributionsToHost(host, contributor, "@mirrorstack/user-core"); err != nil {
		t.Fatalf("explicit catalog reference compatibility = %v", err)
	}
}
