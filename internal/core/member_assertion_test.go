package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
)

const (
	testAssertionAppID  = "a-456"
	testAssertionUserID = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	testAssertionToken  = "msm1.eyJ0eXAiOiJtZW1iZXIifQ.v3qShrO11hUS3ADd1nkukIeammYOYxJdKKBGbP-k3Ws"
)

// okAssertionResponse is the shape a healthy platform returns. Tests mutate a
// copy of it so a failure case differs from success in exactly one field.
func okAssertionResponse() memberAssertionResponse {
	return memberAssertionResponse{
		Token:     testAssertionToken,
		ExpiresAt: "2026-08-16T13:04:05Z",
		ExpiresIn: 300,
	}
}

// assertionRecord is what actually went on the wire. Deliberately NOT reusing
// mintRecord from delivery_test.go: the envelopes differ, and asserting the
// delivery envelope's fields here would silently pass on an empty body.
type assertionRecord struct {
	method  string
	path    string
	appID   string
	secret  string
	body    memberAssertionRequest
	rawBody []byte
	calls   int
}

func newAssertionServer(t *testing.T, handler func(w http.ResponseWriter, rec *assertionRecord)) *assertionRecord {
	t.Helper()
	rec := &assertionRecord{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.calls++
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.appID = r.Header.Get("X-MS-App-ID")
		rec.secret = r.Header.Get("X-MS-Service-Secret")
		rec.rawBody, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(rec.rawBody, &rec.body)
		handler(w, rec)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MS_DISPATCH_URL", srv.URL)
	return rec
}

func writeAssertion(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func assertionCtx() context.Context {
	return auth.Set(context.Background(), auth.Identity{AppID: testAssertionAppID})
}

func TestResolveMemberAssertionURL_Building(t *testing.T) {
	cases := []struct {
		name     string
		dispatch string
		want     string
	}{
		{"dev fallback when unset", "", devDispatchFallback + "/apps/a-456/member-assertions"},
		{"explicit base", "http://dispatch:8083", "http://dispatch:8083/apps/a-456/member-assertions"},
		{"trailing slash trimmed", "http://dispatch:8083/", "http://dispatch:8083/apps/a-456/member-assertions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MS_DISPATCH_URL", tc.dispatch)
			if got := resolveMemberAssertionURL(context.Background(), testAssertionAppID); got != tc.want {
				t.Errorf("resolveMemberAssertionURL = %q, want %q", got, tc.want)
			}
		})
	}
}

// A task attempt's broker-attested routing must WIN over process state. A warm
// Lambda can run concurrent attempts, so routing can never be installed through
// an environment variable — and dispatchBaseFor(ctx) degrading to dispatchBase()
// is otherwise a silent change that no other assertion in this file can see.
func TestResolveMemberAssertionURL_TaskRoutingBeatsProcessState(t *testing.T) {
	t.Setenv("MS_DISPATCH_URL", "http://process-global:8083")
	ctx := runtime.WithTaskDispatchURL(context.Background(), "http://attempt-scoped:9090/")

	want := "http://attempt-scoped:9090/apps/" + testAssertionAppID + "/member-assertions"
	if got := resolveMemberAssertionURL(ctx, testAssertionAppID); got != want {
		t.Errorf("resolveMemberAssertionURL = %q, want %q", got, want)
	}
}

// The happy path, and the wire request that produced it. The credential is a
// header, never a body field: the platform derives the ASSERTING module from it
// and checks that module against the app's recorded auth provider, so there must
// be nothing in the envelope a caller could point elsewhere.
func TestMintMemberAssertion_MintsAndSendsAnIdentityFreeEnvelope(t *testing.T) {
	rec := newAssertionServer(t, func(w http.ResponseWriter, _ *assertionRecord) {
		writeAssertion(w, http.StatusOK, okAssertionResponse())
	})
	t.Setenv("MS_INTERNAL_SECRET", "module-credential")

	got, err := MintMemberAssertion(assertionCtx(), testAssertionUserID, 5*time.Minute)
	if err != nil {
		t.Fatalf("MintMemberAssertion: %v", err)
	}

	if got.Token != testAssertionToken {
		t.Errorf("Token = %q, want %q", got.Token, testAssertionToken)
	}
	if got.ExpiresIn != 5*time.Minute {
		t.Errorf("ExpiresIn = %s, want 5m (the CLAMPED value the platform granted)", got.ExpiresIn)
	}
	if got.ExpiresAt.IsZero() {
		t.Error("ExpiresAt is zero; a holder cannot schedule a renewal")
	}
	if rec.method != http.MethodPost {
		t.Errorf("method = %q, want POST", rec.method)
	}
	if rec.path != "/apps/"+testAssertionAppID+"/member-assertions" {
		t.Errorf("path = %q, want /apps/%s/member-assertions", rec.path, testAssertionAppID)
	}
	if rec.appID != testAssertionAppID {
		t.Errorf("X-MS-App-ID = %q, want %q", rec.appID, testAssertionAppID)
	}
	if rec.secret != "module-credential" {
		t.Errorf("X-MS-Service-Secret = %q, want the module credential", rec.secret)
	}
	if rec.body.V != memberAssertionEnvelopeVersion {
		t.Errorf("envelope v = %d, want %d", rec.body.V, memberAssertionEnvelopeVersion)
	}
	if rec.body.UserID != testAssertionUserID {
		t.Errorf("envelope userId = %q, want %q", rec.body.UserID, testAssertionUserID)
	}
	// A module id in the envelope would let any module claim the auth-provider
	// right; an app id would let it assert into another app. Neither field may
	// exist on the wire at all — identity comes from the credential and the URL.
	var raw map[string]any
	if err := json.Unmarshal(rec.rawBody, &raw); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	for _, forbidden := range []string{"module", "moduleID", "moduleId", "app", "appID", "appId"} {
		if _, ok := raw[forbidden]; ok {
			t.Errorf("envelope carries %q; identity must come from the credential and the URL only", forbidden)
		}
	}
	// No role either: identity is not authorization, and a role in the mint
	// envelope would be a module deciding its own privilege.
	for _, forbidden := range []string{"role", "appRole", "scope", "scopes", "permissions"} {
		if _, ok := raw[forbidden]; ok {
			t.Errorf("envelope carries %q; an assertion says WHO, never what they may do", forbidden)
		}
	}
}

// 🔴 THE FAIL-CLOSED TEST. Every way minting can fail must produce an error and
// an EMPTY token. A non-empty token alongside an error would be attached to real
// requests: dispatch would reject it and every module downstream would see an
// anonymous caller, so a member who believes they are signed in gets the
// signed-out answer and their audit rows name no actor.
func TestMintMemberAssertion_MintFailureReturnsAnErrorAndNoToken(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    any
		noServe bool   // reply something unparseable: a broken or wrong-version dispatch
		raw     string // reply these exact bytes instead of encoding body
	}{
		{name: "signing key unconfigured", status: http.StatusServiceUnavailable,
			body: map[string]any{"error": map[string]string{"code": "token_signing_unavailable", "message": "member assertions are not available"}}},
		{name: "caller is not this app's auth provider", status: http.StatusForbidden,
			body: map[string]any{"error": map[string]string{"code": "not_auth_provider", "message": "sender does not host the auth-provider slot"}}},
		{name: "sender not recognised", status: http.StatusForbidden,
			body: map[string]any{"error": map[string]string{"code": "unknown_sender", "message": "sender is not a live module"}}},
		{name: "app not found", status: http.StatusNotFound,
			body: map[string]any{"error": map[string]string{"code": "app_not_found", "message": "app not found"}}},
		// Until #518 PR 1 deploys, this is the response a real platform gives.
		{name: "endpoint does not exist yet", status: http.StatusNotFound, body: nil, noServe: true},
		{name: "user id rejected", status: http.StatusBadRequest,
			body: map[string]any{"error": map[string]string{"code": "invalid_user", "message": "userId must be a uuid"}}},
		{name: "unparseable success body", status: http.StatusOK, body: nil, noServe: true},
		// 🔴 A DECODE ERROR CAN STILL LEAVE A TOKEN IN THE STRUCT. encoding/json
		// keeps going past a type error and populates the fields it could, so a
		// platform that returns expiresIn as a string hands the transport an
		// error AND a populated Token. Anything that carried that token out
		// alongside the error would attach an unverifiable credential to real
		// requests. The zero value is the only correct return.
		{name: "200 that half-decodes, leaving a token behind", status: http.StatusOK,
			raw: `{"token":"leaked-partial-token","expiresAt":"2026-08-16T13:04:05Z","expiresIn":"300"}`},
		{name: "200 with an empty token", status: http.StatusOK, body: func() memberAssertionResponse {
			r := okAssertionResponse()
			r.Token = ""
			return r
		}()},
		{name: "200 with a token carrying CRLF", status: http.StatusOK, body: func() memberAssertionResponse {
			r := okAssertionResponse()
			r.Token = "msm1.abc\r\nX-MS-App-Role: owner"
			return r
		}()},
		{name: "200 with a token carrying a space", status: http.StatusOK, body: func() memberAssertionResponse {
			r := okAssertionResponse()
			r.Token = "msm1.abc def"
			return r
		}()},
		{name: "200 with an oversized token", status: http.StatusOK, body: func() memberAssertionResponse {
			r := okAssertionResponse()
			r.Token = "msm1." + strings.Repeat("a", 5000)
			return r
		}()},
		{name: "200 with a zero lifetime", status: http.StatusOK, body: func() memberAssertionResponse {
			r := okAssertionResponse()
			r.ExpiresIn = 0
			return r
		}()},
		{name: "200 with a negative lifetime", status: http.StatusOK, body: func() memberAssertionResponse {
			r := okAssertionResponse()
			r.ExpiresIn = -1
			return r
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newAssertionServer(t, func(w http.ResponseWriter, _ *assertionRecord) {
				if tc.noServe || tc.raw != "" {
					payload := tc.raw
					if payload == "" {
						payload = "not json at all"
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(payload))
					return
				}
				writeAssertion(w, tc.status, tc.body)
			})

			got, err := MintMemberAssertion(assertionCtx(), testAssertionUserID, 5*time.Minute)
			if err == nil {
				t.Fatalf("MintMemberAssertion succeeded on a failed mint and returned %+v", got)
			}
			// The load-bearing assertion: not merely "an error", but NOTHING a
			// caller could attach to a request.
			if got.Token != "" {
				t.Fatalf("MintMemberAssertion returned token %q alongside its error; a failed mint must yield NO assertion", got.Token)
			}
			if got.ExpiresIn != 0 || !got.ExpiresAt.IsZero() {
				t.Fatalf("failed mint returned a live-looking assertion %+v, want the zero value", got)
			}
		})
	}
}

// DeliveryTicket's fail-closed guarantee has two layers — response screening
// plus the armed check inside URL — so a mutation to either alone still fails
// closed. A MemberAssertion has only ONE, because the token must be exported for
// the auth module to hand onward. This test pins that single layer directly, so
// a mutation that deletes it is visible here and not only through the mint path.
func TestNewMemberAssertion_RejectsAnUnusableMintResponse(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*memberAssertionResponse)
		wantErr string // substring the error must carry; "" = must succeed
	}{
		{name: "healthy response", mutate: func(*memberAssertionResponse) {}},
		// 🔴 "the platform returned NOTHING" and "the platform returned GARBAGE"
		// are different bugs in different places, so they must not collapse into
		// one message. Asserting the wording is also what keeps the explicit
		// empty check alive: actor.ValidTransportValue happens to reject "" too,
		// so without this the line would be invisible to every test — and this
		// file's emptiness guarantee would silently depend on another package's
		// transport rule never relaxing.
		{name: "empty token", mutate: func(r *memberAssertionResponse) { r.Token = "" }, wantErr: "returned no assertion"},
		// The token rides an Authorization header. A CR/LF would let a broken or
		// hostile response append headers of its own to every request the
		// assertion is attached to.
		{name: "token carrying CR", mutate: func(r *memberAssertionResponse) { r.Token = "msm1.abc\rdef" }, wantErr: "not header-safe"},
		{name: "token carrying LF", mutate: func(r *memberAssertionResponse) { r.Token = "msm1.abc\ndef" }, wantErr: "not header-safe"},
		{name: "token carrying a tab", mutate: func(r *memberAssertionResponse) { r.Token = "msm1.abc\tdef" }, wantErr: "not header-safe"},
		{name: "token carrying a space", mutate: func(r *memberAssertionResponse) { r.Token = "msm1.abc def" }, wantErr: "not header-safe"},
		{name: "token carrying a comma", mutate: func(r *memberAssertionResponse) { r.Token = "msm1.abc,def" }, wantErr: "not header-safe"},
		{name: "token over the transport cap", mutate: func(r *memberAssertionResponse) { r.Token = strings.Repeat("a", 4097) }, wantErr: "not header-safe"},
		{name: "token exactly at the transport cap is fine", mutate: func(r *memberAssertionResponse) { r.Token = strings.Repeat("a", 4096) }},
		{name: "zero lifetime", mutate: func(r *memberAssertionResponse) { r.ExpiresIn = 0 }, wantErr: "non-positive assertion lifetime"},
		{name: "negative lifetime", mutate: func(r *memberAssertionResponse) { r.ExpiresIn = -1 }, wantErr: "non-positive assertion lifetime"},
		// The token FORMAT is deliberately not pinned: dispatch owns it, and
		// asserting msm1 here would make a future msm2 a breaking SDK change.
		{name: "a future token version is accepted, not second-guessed",
			mutate: func(r *memberAssertionResponse) { r.Token = "msm2.abc.def" }},
		{name: "unparseable expiresAt falls back rather than failing a good mint",
			mutate: func(r *memberAssertionResponse) { r.ExpiresAt = "not a timestamp" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := okAssertionResponse()
			tc.mutate(&resp)

			got, err := newMemberAssertion(resp)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("newMemberAssertion accepted an unusable response and returned %+v", got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to name the actual defect (%q)", err, tc.wantErr)
				}
				if got.Token != "" {
					t.Fatalf("rejected response still yielded token %q", got.Token)
				}
				return
			}
			if err != nil {
				t.Fatalf("newMemberAssertion: %v", err)
			}
			if got.Token != resp.Token {
				t.Errorf("Token = %q, want %q", got.Token, resp.Token)
			}
			if got.ExpiresIn != time.Duration(resp.ExpiresIn)*time.Second {
				t.Errorf("ExpiresIn = %s, want %ds", got.ExpiresIn, resp.ExpiresIn)
			}
			if got.ExpiresAt.IsZero() {
				t.Error("ExpiresAt is zero; a holder cannot schedule a renewal")
			}
		})
	}
}

// A module must not be able to assert a FREE-FORM principal. These are rejected
// locally, in the caller's own stack frame, with ZERO round trips — the platform
// re-checks, but a caller should not need a 400 to learn it passed a username.
func TestMintMemberAssertion_RejectsANonUUIDPrincipalWithoutCallingThePlatform(t *testing.T) {
	cases := []string{
		"",
		"admin",
		"*",
		"me@example.com",
		"7c9e6679-7425-40de-944b-e07fc1f90ae",   // one char short
		"7c9e6679-7425-40de-944b-e07fc1f90ae77", // one char long
		"7c9e6679742540de944be07fc1f90ae7",      // unhyphenated
		" 7c9e6679-7425-40de-944b-e07fc1f90ae7",
		"7c9e6679-7425-40de-944b-e07fc1f90ae7 ",
		"7c9e6679-7425-40de-944b-e07fc1f90ae7\nX-MS-User-ID: someone-else",
		"7c9e6679-7425-40de-944b-e07fc1z90ae7", // 'z' is not hex
	}

	for _, userID := range cases {
		t.Run(strings.ReplaceAll(userID, "\n", "\\n"), func(t *testing.T) {
			rec := newAssertionServer(t, func(w http.ResponseWriter, _ *assertionRecord) {
				writeAssertion(w, http.StatusOK, okAssertionResponse())
			})

			got, err := MintMemberAssertion(assertionCtx(), userID, 5*time.Minute)
			if err == nil {
				t.Fatalf("MintMemberAssertion(%q) succeeded and returned %+v", userID, got)
			}
			if got.Token != "" {
				t.Errorf("returned token %q, want none", got.Token)
			}
			if rec.calls != 0 {
				t.Errorf("rejected locally but still made %d mint call(s)", rec.calls)
			}
		})
	}
}

// ttl is a PROPOSAL and travels as whole seconds. "Unset" must stay
// distinguishable from an explicit 0 on the wire, or the platform's default
// silently replaces a caller's intent. Asserted on the RAW JSON, not the decoded
// struct: a decoded *int64 cannot tell an absent field from a null one.
func TestMintMemberAssertion_TTLPassthrough(t *testing.T) {
	cases := []struct {
		name    string
		ttl     time.Duration
		want    string // raw JSON value expected for ttlSeconds; "" = field absent
		wantErr bool
	}{
		{name: "the five-minute default lifetime", ttl: 5 * time.Minute, want: "300"},
		{name: "above the platform ceiling is still sent verbatim and clamped there", ttl: time.Hour, want: "3600"},
		{name: "zero omits the field so the platform default applies", ttl: 0, want: ""},
		{name: "sub-second rounds up to an explicit 1s, never to absent", ttl: 500 * time.Millisecond, want: "1"},
		{name: "fractional rounds up", ttl: 90500 * time.Millisecond, want: "91"},
		{name: "negative is a caller bug, not a default", ttl: -time.Second, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newAssertionServer(t, func(w http.ResponseWriter, _ *assertionRecord) {
				writeAssertion(w, http.StatusOK, okAssertionResponse())
			})

			_, err := MintMemberAssertion(assertionCtx(), testAssertionUserID, tc.ttl)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ttl %s: want an error", tc.ttl)
				}
				if rec.calls != 0 {
					t.Errorf("ttl %s: rejected locally but still made %d mint call(s)", tc.ttl, rec.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("ttl %s: %v", tc.ttl, err)
			}

			var raw map[string]json.RawMessage
			if err := json.Unmarshal(rec.rawBody, &raw); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			field, present := raw["ttlSeconds"]
			if tc.want == "" {
				if present {
					t.Errorf("ttl %s: ttlSeconds = %s, want the field to be ABSENT", tc.ttl, field)
				}
				return
			}
			if !present {
				t.Fatalf("ttl %s: ttlSeconds absent, want %s", tc.ttl, tc.want)
			}
			if string(field) != tc.want {
				t.Errorf("ttl %s: ttlSeconds = %s, want %s", tc.ttl, field, tc.want)
			}
		})
	}
}

// An app-scoped context is required: the mint URL is app-scoped and the platform
// re-resolves the app from it, so an unscoped call would mint against whatever
// app the path happened to name.
func TestMintMemberAssertion_RequiresAnAppScopedContext(t *testing.T) {
	rec := newAssertionServer(t, func(w http.ResponseWriter, _ *assertionRecord) {
		writeAssertion(w, http.StatusOK, okAssertionResponse())
	})

	got, err := MintMemberAssertion(context.Background(), testAssertionUserID, 5*time.Minute)
	if err == nil {
		t.Fatalf("MintMemberAssertion without an app scope returned %+v, want an error", got)
	}
	if got.Token != "" {
		t.Errorf("returned token %q, want none", got.Token)
	}
	if rec.calls != 0 {
		t.Errorf("made %d mint call(s) with no app scope, want 0", rec.calls)
	}
}

// The zero value is what every error path returns, and it must be unusable on
// its own — a caller that ignores the error still has nothing to attach.
func TestMemberAssertion_ZeroValueCarriesNoCredential(t *testing.T) {
	var zero MemberAssertion
	if zero.Token != "" {
		t.Errorf("zero MemberAssertion carries token %q, want none", zero.Token)
	}
	if zero.ExpiresIn != 0 || !zero.ExpiresAt.IsZero() {
		t.Errorf("zero MemberAssertion looks live: %+v", zero)
	}
}
