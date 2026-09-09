package db

import (
	"os"
	"testing"
)

// apiPlatformRenderedURL is the EXACT string api-platform's
// modulehost.ModuleDBEndpoint.URL() writes into a deployed module's
// DATABASE_URL (api-platform#756). It is duplicated here deliberately: this
// test exists to pin a CROSS-REPO seam, and a seam asserted against a value
// imported from one side is only asserting that side agrees with itself.
//
// 🔴 Why this test exists. The lifecycle body carries ONLY username+token; the
// LOCATION comes from the module's own environment via EnvBaseCredential. The
// production half of that resolution was never written — see the comment on
// EnvBaseCredential — so it fell through to defaultDevURL, and every lifecycle
// call against a deployed module died with
// "dial tcp 127.0.0.1:5433: connect: connection refused". No deployed module's
// app-scope migrations had ever run.
//
// api-platform now supplies DATABASE_URL. Nothing on THIS side of the seam
// tested that the SDK actually consumes it, and the two halves live in
// different repos, so a format the writer considers obvious and the reader
// rejects would reproduce the same outage with a different error string.
const apiPlatformRenderedURL = "postgres://module@aurora.example:5432/mirrorstack?sslmode=require"

func TestEnvBaseCredentialConsumesDeployedDatabaseURL(t *testing.T) {
	t.Setenv("MS_LOCAL_DB_URL", "")
	t.Setenv("DATABASE_URL", apiPlatformRenderedURL)

	got, err := EnvBaseCredential()
	if err != nil {
		t.Fatalf("EnvBaseCredential rejected the URL api-platform emits: %v", err)
	}
	if got.Host != "aurora.example" || got.Port != 5432 || got.Database != "mirrorstack" {
		t.Fatalf("host/port/database = %q/%d/%q, want aurora.example/5432/mirrorstack", got.Host, got.Port, got.Database)
	}
	// The placeholder user in the rendered URL must NOT survive as a
	// credential: the lifecycle handler overwrites Username and Token from the
	// request body, and a base that carried "module" through would mask a
	// platform that forgot to send one.
	if got.Username != "" || got.Token != "" {
		t.Errorf("base credential carried identity through: username=%q token=%q", got.Username, got.Token)
	}
}

// TestEnvBaseCredentialFallsBackToDevWithoutDatabaseURL is the NEGATIVE
// CONTROL. Without it the test above passes for a build that ignores the env
// entirely and happens to be pointed at a matching default — the same vacuity
// that let the original bug ship. This pins that the dev fallback is what the
// deployed plane was actually getting.
func TestEnvBaseCredentialFallsBackToDevWithoutDatabaseURL(t *testing.T) {
	t.Setenv("MS_LOCAL_DB_URL", "")
	t.Setenv("DATABASE_URL", "")

	got, err := EnvBaseCredential()
	if err != nil {
		t.Fatalf("EnvBaseCredential: %v", err)
	}
	if got.Host != "localhost" || got.Port != 5433 {
		t.Fatalf("fallback = %s:%d, want localhost:5433 — if this changed, the "+
			"production diagnosis keyed on that fingerprint needs revisiting", got.Host, got.Port)
	}
}

// TestMSLocalDBURLStillWinsOverDatabaseURL pins the precedence the CLI relies
// on: `mirrorstack dev` exports MS_LOCAL_DB_URL, and a developer running a
// module locally must not be silently redirected at a production endpoint if
// DATABASE_URL is also present in their shell.
func TestMSLocalDBURLStillWinsOverDatabaseURL(t *testing.T) {
	t.Setenv("MS_LOCAL_DB_URL", "postgres://mirrorstack:mirrorstack@localhost:5433/mirrorstack?sslmode=disable")
	t.Setenv("DATABASE_URL", apiPlatformRenderedURL)

	got, err := EnvBaseCredential()
	if err != nil {
		t.Fatalf("EnvBaseCredential: %v", err)
	}
	if got.Host != "localhost" || got.Port != 5433 {
		t.Fatalf("MS_LOCAL_DB_URL lost to DATABASE_URL: got %s:%d — a local dev run "+
			"would connect to the deployed endpoint", got.Host, got.Port)
	}
}

var _ = os.Getenv
