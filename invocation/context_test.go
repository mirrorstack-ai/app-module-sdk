package invocation_test

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationwire"
	"github.com/mirrorstack-ai/app-module-sdk/invocation"
)

func fixture() invocation.Context {
	return invocation.Context{
		Version: invocation.Version,
		App: invocation.App{
			ID:     "22222222-2222-4222-8222-222222222222",
			Schema: "app_22222222_2222_4222_8222_222222222222",
		},
		Module: invocation.Module{ID: "11111111-1111-4111-8111-111111111111", Slug: "user-core"},
		Identity: invocation.Identity{
			Kind: invocation.IdentityKindPlatform, UserID: "33333333-3333-4333-8333-333333333333", AppRole: "admin",
			ActorDelegation: "msa1.fixture.signature",
		},
		Routes: invocation.Routes{
			Origin:       "https://api.mirrorstack.ai",
			Module:       "/v1/apps/app/22222222-2222-4222-8222-222222222222",
			Public:       "/v1/apps/app/22222222-2222-4222-8222-222222222222/public/user-core",
			Platform:     "/v1/apps/app/22222222-2222-4222-8222-222222222222/platform/user-core",
			CurrentLocal: "/platform/users/44444444-4444-4444-8444-444444444444?tab=profile",
			Redirects:    []string{"https://app.example/callback", "https://app.example/callback,secondary"},
		},
		Request: invocation.Request{
			ID: "55555555-5555-4555-8555-555555555555", Method: http.MethodGet,
			Path:       "/platform/users/44444444-4444-4444-8444-444444444444?tab=profile",
			BodySHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			OccurredAt: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
		},
		Trust: invocation.Trust{
			Source: invocation.TrustSourceEdge, ClientIP: "203.0.113.7", Host: "api.mirrorstack.ai", Scheme: "https", Origin: "https://api.mirrorstack.ai",
		},
		Cookies: invocation.Cookies{
			Namespace:       "v1:22222222-2222-4222-8222-222222222222:11111111-1111-4111-8111-111111111111",
			PhysicalPrefix:  "__msm1_ylmzrmjolm45autoczka_",
			Capabilities:    []string{invocation.CookieCapabilityLegacyLogicalRead, invocation.CookieCapabilityPhysicalNamesV1},
			LegacyReadUntil: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC),
		},
		Audit: invocation.Audit{Provenance: "mip1.e30.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
	}
}

func TestV1FixtureMatchesAPIPlatform(t *testing.T) {
	want, err := os.ReadFile("testdata/invocation_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := invocationwire.Marshal(fixture())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, bytes.TrimSpace(want)) {
		t.Fatalf("canonical fixture drifted\n got: %s\nwant: %s", raw, bytes.TrimSpace(want))
	}
	header, err := invocationwire.EncodeHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	decoded, decodedRaw, err := invocationwire.DecodeHeader(header)
	if err != nil || decoded.Request.ID != fixture().Request.ID || !bytes.Equal(decodedRaw, raw) {
		t.Fatalf("header round trip: decoded=%+v bytes_equal=%v err=%v", decoded, bytes.Equal(decodedRaw, raw), err)
	}
}

// 🔴 The canonical-routes rule is a TRANSCRIPTION of api-platform
// internal/shared/moduleinvoke.ModuleRoutes. TestV1FixtureMatchesAPIPlatform
// above compares whole fixtures, which catches drift only while both repos edit
// the same golden; this pins the RULE itself, in the form a reader of either
// repo can compare by eye, and proves the validator actually rejects the shape
// this release replaces. Without the negative case the check is vacuous: a
// validator that accepted everything would pass the positive one.
func TestCanonicalRoutesAreScopeBeforeModule(t *testing.T) {
	const app = "22222222-2222-4222-8222-222222222222"

	valid := fixture()
	if valid.Routes.Module != "/v1/apps/app/"+app ||
		valid.Routes.Public != "/v1/apps/app/"+app+"/public/user-core" ||
		valid.Routes.Platform != "/v1/apps/app/"+app+"/platform/user-core" {
		t.Fatalf("fixture routes are not the canonical shape: %+v", valid.Routes)
	}
	if _, err := invocationwire.Marshal(valid); err != nil {
		t.Fatalf("canonical routes rejected: %v", err)
	}

	// The retired shape: app first, scope appended to a single module base.
	for name, mutate := range map[string]func(*invocation.Routes){
		"retired module-first shape": func(r *invocation.Routes) {
			base := "/v1/dispatch/apps/" + app + "/user-core"
			r.Module, r.Public, r.Platform = base, base+"/public", base+"/platform"
		},
		"scope after module": func(r *invocation.Routes) {
			r.Public = "/v1/apps/app/" + app + "/user-core/public"
		},
		"module base carrying a scope": func(r *invocation.Routes) {
			r.Module = "/v1/apps/app/" + app + "/public/user-core"
		},
		"wrong module slug in a scope": func(r *invocation.Routes) {
			r.Platform = "/v1/apps/app/" + app + "/platform/oauth-google"
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := fixture()
			mutate(&bad.Routes)
			if _, err := invocationwire.Marshal(bad); err == nil {
				t.Fatalf("routes %+v were accepted; the canonical check is not running", bad.Routes)
			}
		})
	}
}

func TestStrictDecodeAndLegacyConflictsFailClosed(t *testing.T) {
	raw, err := invocationwire.Marshal(fixture())
	if err != nil {
		t.Fatal(err)
	}
	malformed := map[string][]byte{
		"unknown field":  bytes.Replace(raw, []byte(`"v":1`), []byte(`"v":1,"future":true`), 1),
		"non canonical":  append([]byte(" "), raw...),
		"trailing value": append(append([]byte(nil), raw...), []byte(`{}`)...),
	}
	for name, value := range malformed {
		t.Run(name, func(t *testing.T) {
			if _, err := invocationwire.Parse(value); err == nil {
				t.Fatal("malformed context was accepted")
			}
		})
	}
	if _, _, err := invocationwire.DecodeHeader("not+raw-url-base64"); err == nil {
		t.Fatal("malformed header encoding was accepted")
	}
	trusted := fixture()
	if err := invocationwire.ValidateLegacyHeaders(trusted, map[string]string{invocationwire.LegacyAppIDHeader: "forged"}); err == nil {
		t.Fatal("conflicting legacy app was accepted")
	}
	if err := invocationwire.ValidateLegacyHeaders(trusted, map[string]string{invocationwire.LegacyAppIDHeader: trusted.App.ID}); err != nil {
		t.Fatalf("matching compatibility value rejected: %v", err)
	}
	if err := invocationwire.ValidateLegacyHeaders(trusted, map[string]string{
		invocationwire.LegacyAppIDHeader:                  trusted.App.ID,
		strings.ToLower(invocationwire.LegacyAppIDHeader): trusted.App.ID,
	}); err == nil {
		t.Fatal("case-variant duplicate compatibility values were accepted")
	}
}

func TestCrossFieldConflictsFailClosed(t *testing.T) {
	mutations := map[string]func(*invocation.Context){
		"app schema":   func(value *invocation.Context) { value.App.Schema = "app_forged" },
		"module route": func(value *invocation.Context) { value.Routes.Module = "/forged" },
		"current path": func(value *invocation.Context) { value.Routes.CurrentLocal = "/different" },
		"body digest":  func(value *invocation.Context) { value.Request.BodySHA256 = strings.Repeat("0", 63) },
		"HTTP method":  func(value *invocation.Context) { value.Request.Method = "GET\r\nX-Forged: yes" },
		"trusted origin": func(value *invocation.Context) {
			value.Trust.Origin += "?forged=1"
			value.Routes.Origin = value.Trust.Origin
		},
		"redirect duplicate": func(value *invocation.Context) {
			value.Routes.Redirects = append(value.Routes.Redirects, value.Routes.Redirects[0])
		},
		"redirect null":     func(value *invocation.Context) { value.Routes.Redirects = nil },
		"cookie namespace":  func(value *invocation.Context) { value.Cookies.Namespace = "v1:forged" },
		"cookie capability": func(value *invocation.Context) { value.Cookies.Capabilities = []string{"future", "physical-names-v1"} },
		"expired migration": func(value *invocation.Context) { value.Request.OccurredAt = value.Cookies.LegacyReadUntil },
		"platform role":     func(value *invocation.Context) { value.Identity.AppRole = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			trusted := fixture()
			mutate(&trusted)
			if _, err := invocationwire.Marshal(trusted); err == nil {
				t.Fatal("conflicting context was accepted")
			}
		})
	}
}

func TestContextCopiesSlicesAndBodyBinding(t *testing.T) {
	trusted := fixture()
	ctx := invocationwire.WithContext(context.Background(), trusted)
	if proof := invocationwire.ProofFromContext(ctx); proof != nil {
		t.Fatalf("synthetic typed context unexpectedly carried proof: %q", proof)
	}
	trusted.Routes.Redirects[0] = "https://mutated.example"
	trusted.Cookies.Capabilities[0] = "mutated"

	got, ok := invocation.FromContext(ctx)
	if !ok || got.Routes.Redirects[0] != "https://app.example/callback" || got.Cookies.Capabilities[0] != invocation.CookieCapabilityLegacyLogicalRead {
		t.Fatalf("stored context was mutable: %+v", got)
	}
	got.Routes.Redirects[0] = "https://second-mutation.example"
	again, _ := invocation.FromContext(ctx)
	if again.Routes.Redirects[0] != "https://app.example/callback" {
		t.Fatalf("loaded context shared its slice: %+v", again.Routes.Redirects)
	}
	if !invocationwire.BodyMatches(got, nil) || invocationwire.BodyMatches(got, []byte("different")) {
		t.Fatal("request body digest binding is incorrect")
	}
}
