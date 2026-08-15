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
)

const (
	testDeliveryAppID    = "a-456"
	testDeliveryPrefix   = "_gated/videos/v1/transcodes/j1/720p/"
	testDeliveryKey      = testDeliveryPrefix + "seg_000042.ts"
	testDeliveryToken    = "bXMtY2RuLXRva2VuOnYx.v3qShrO11hUS3ADd1nkukIeammYOYxJdKKBGbP-k3Ws"
	testDeliveryComposed = "apps/1f0e2c3d-4a5b-6c7d-8e9f-0a1b2c3d4e5f/2a9d8c7b-6e5f-4a3b-2c1d-0e9f8a7b6c5d/" + testDeliveryPrefix
	testDeliveryCDNBase  = "https://cdn.mirrorstack.ai"
)

// okMintResponse is the shape a healthy platform returns. Tests mutate a copy
// of it so a failure case differs from success in exactly one field.
func okMintResponse() deliveryMintResponse {
	return deliveryMintResponse{
		Token:     testDeliveryToken,
		Param:     "mst",
		Prefix:    testDeliveryComposed,
		URLPrefix: testDeliveryCDNBase + "/" + testDeliveryComposed,
		ExpiresAt: "2026-08-16T13:04:05Z",
		ExpiresIn: 3600,
	}
}

// mintServer stands in for dispatch. handler decides the reply; the recorded
// request is returned so a test can assert what actually went on the wire.
type mintRecord struct {
	method  string
	path    string
	appID   string
	secret  string
	body    deliveryMintRequest
	rawBody []byte
	calls   int
}

func newMintServer(t *testing.T, handler func(w http.ResponseWriter, rec *mintRecord)) *mintRecord {
	t.Helper()
	rec := &mintRecord{}
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

func writeMint(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func deliveryCtx() context.Context {
	return auth.Set(context.Background(), auth.Identity{AppID: testDeliveryAppID})
}

func TestResolveDeliveryMintURL_Building(t *testing.T) {
	cases := []struct {
		name     string
		dispatch string
		want     string
	}{
		{"dev fallback when unset", "", devDispatchFallback + "/apps/a-456/cdn-tokens"},
		{"explicit base", "http://dispatch:8083", "http://dispatch:8083/apps/a-456/cdn-tokens"},
		{"trailing slash trimmed", "http://dispatch:8083/", "http://dispatch:8083/apps/a-456/cdn-tokens"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MS_DISPATCH_URL", tc.dispatch)
			if got := resolveDeliveryMintURL(context.Background(), testDeliveryAppID); got != tc.want {
				t.Errorf("resolveDeliveryMintURL = %q, want %q", got, tc.want)
			}
		})
	}
}

// The happy path, and the wire request that produced it. The credential is a
// header, never a body field: the platform derives the module from it, so
// there must be nothing in the envelope a caller could point elsewhere.
func TestDeliveryURL_MintsAndAttachesTheCredential(t *testing.T) {
	rec := newMintServer(t, func(w http.ResponseWriter, _ *mintRecord) {
		writeMint(w, http.StatusOK, okMintResponse())
	})
	t.Setenv("MS_INTERNAL_SECRET", "module-credential")

	got, err := DeliveryURL(deliveryCtx(), testDeliveryKey, time.Hour)
	if err != nil {
		t.Fatalf("DeliveryURL: %v", err)
	}

	want := testDeliveryCDNBase + "/" + testDeliveryComposed + "seg_000042.ts?mst=" + testDeliveryToken
	if got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
	if rec.method != http.MethodPost {
		t.Errorf("method = %q, want POST", rec.method)
	}
	if rec.path != "/apps/"+testDeliveryAppID+"/cdn-tokens" {
		t.Errorf("path = %q, want /apps/%s/cdn-tokens", rec.path, testDeliveryAppID)
	}
	if rec.appID != testDeliveryAppID {
		t.Errorf("X-MS-App-ID = %q, want %q", rec.appID, testDeliveryAppID)
	}
	if rec.secret != "module-credential" {
		t.Errorf("X-MS-Service-Secret = %q, want the module credential", rec.secret)
	}
	if rec.body.V != deliveryEnvelopeVersion {
		t.Errorf("envelope v = %d, want %d", rec.body.V, deliveryEnvelopeVersion)
	}
	// The mint is scoped to the key's own directory, not to "_gated/".
	if rec.body.Prefix != testDeliveryPrefix {
		t.Errorf("minted prefix = %q, want %q", rec.body.Prefix, testDeliveryPrefix)
	}
	// A module id in the envelope would be a scope-escape seam. There must be
	// no such field on the wire at all.
	var raw map[string]any
	if err := json.Unmarshal(rec.rawBody, &raw); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	for _, forbidden := range []string{"module", "moduleID", "moduleId", "app", "appID", "appId"} {
		if _, ok := raw[forbidden]; ok {
			t.Errorf("envelope carries %q; identity must come from the credential and the URL only", forbidden)
		}
	}
}

// A ticket is minted once and serves many objects — the property that makes
// this usable for a transcode job's hundreds of segments.
func TestDelivery_OneMintServesEveryObjectUnderThePrefix(t *testing.T) {
	rec := newMintServer(t, func(w http.ResponseWriter, _ *mintRecord) {
		writeMint(w, http.StatusOK, okMintResponse())
	})

	ticket, err := Delivery(deliveryCtx(), testDeliveryPrefix, time.Hour)
	if err != nil {
		t.Fatalf("Delivery: %v", err)
	}
	if ticket.ExpiresIn != time.Hour {
		t.Errorf("ExpiresIn = %s, want 1h (the CLAMPED value the platform granted)", ticket.ExpiresIn)
	}
	if ticket.Prefix != testDeliveryComposed {
		t.Errorf("Prefix = %q, want the platform-composed %q", ticket.Prefix, testDeliveryComposed)
	}

	for _, name := range []string{"seg_000001.ts", "seg_000042.ts", "init.mp4"} {
		got, err := ticket.URL(name)
		if err != nil {
			t.Fatalf("ticket.URL(%q): %v", name, err)
		}
		if want := ticket.URLPrefix + name + "?mst=" + testDeliveryToken; got != want {
			t.Errorf("ticket.URL(%q) = %q, want %q", name, got, want)
		}
	}
	if rec.calls != 1 {
		t.Errorf("mint calls = %d, want exactly 1 for the whole prefix", rec.calls)
	}
}

// 🔴 THE FAIL-CLOSED TEST. Every way minting can fail must produce an error and
// NO URL. A returned string here — even the correct-looking CDN URL — is a
// paywall bypass: unsigned segment URLs are public, and they land in viewers'
// playlists where a rollback cannot reach them.
func TestDeliveryURL_MintFailureReturnsAnErrorAndNoURL(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    any
		noServe bool // reply nothing usable: simulates an unreachable dispatch
	}{
		{name: "signing key unconfigured", status: http.StatusServiceUnavailable,
			body: map[string]any{"error": map[string]string{"code": "token_signing_unavailable", "message": "cdn delivery tokens are not available"}}},
		{name: "sender not recognised", status: http.StatusForbidden,
			body: map[string]any{"error": map[string]string{"code": "unknown_sender", "message": "cdn token sender is not a live module"}}},
		{name: "app not found", status: http.StatusNotFound,
			body: map[string]any{"error": map[string]string{"code": "app_not_found", "message": "app not found"}}},
		{name: "prefix rejected", status: http.StatusBadRequest,
			body: map[string]any{"error": map[string]string{"code": "invalid_prefix", "message": "bad prefix"}}},
		{name: "unparseable success body", status: http.StatusOK, body: nil, noServe: true},
		{name: "200 with an empty token", status: http.StatusOK, body: func() deliveryMintResponse {
			r := okMintResponse()
			r.Token = ""
			return r
		}()},
		{name: "200 with a blank param name", status: http.StatusOK, body: func() deliveryMintResponse {
			r := okMintResponse()
			r.Param = ""
			return r
		}()},
		{name: "200 with a param name that injects query structure", status: http.StatusOK, body: func() deliveryMintResponse {
			r := okMintResponse()
			r.Param = "mst&public=1"
			return r
		}()},
		{name: "200 with a token that is not query-safe", status: http.StatusOK, body: func() deliveryMintResponse {
			r := okMintResponse()
			r.Token = "abc&admin=1"
			return r
		}()},
		{name: "200 with no CDN origin", status: http.StatusOK, body: func() deliveryMintResponse {
			r := okMintResponse()
			r.URLPrefix = ""
			return r
		}()},
		{name: "200 whose url does not end in the composed prefix", status: http.StatusOK, body: func() deliveryMintResponse {
			r := okMintResponse()
			r.URLPrefix = testDeliveryCDNBase + "/somewhere/else/"
			return r
		}()},
		{name: "200 whose composed prefix is broader than requested", status: http.StatusOK, body: func() deliveryMintResponse {
			r := okMintResponse()
			r.Prefix = "apps/1f0e2c3d-4a5b-6c7d-8e9f-0a1b2c3d4e5f/2a9d8c7b-6e5f-4a3b-2c1d-0e9f8a7b6c5d/_gated/"
			r.URLPrefix = testDeliveryCDNBase + "/" + r.Prefix
			return r
		}()},
		{name: "200 with a non-positive lifetime", status: http.StatusOK, body: func() deliveryMintResponse {
			r := okMintResponse()
			r.ExpiresIn = 0
			return r
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newMintServer(t, func(w http.ResponseWriter, _ *mintRecord) {
				if tc.noServe {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte("not json at all"))
					return
				}
				writeMint(w, tc.status, tc.body)
			})

			got, err := DeliveryURL(deliveryCtx(), testDeliveryKey, time.Hour)
			if err == nil {
				t.Fatalf("DeliveryURL succeeded on a failed mint and returned %q", got)
			}
			// The load-bearing assertion: not merely "an error", but NOTHING a
			// caller could hand to a player.
			if got != "" {
				t.Fatalf("DeliveryURL returned %q alongside its error; a mint failure must yield NO url", got)
			}
			if strings.Contains(got, testDeliveryCDNBase) || strings.Contains(got, "http") {
				t.Fatalf("DeliveryURL leaked an unsigned CDN url on failure: %q", got)
			}
		})
	}
}

// The guarantee above holds through TWO independent layers — response screening
// here, and the armed check on DeliveryTicket.URL — so removing either one alone
// still fails closed. This test pins the FIRST layer on its own, so a mutation
// that deletes it is visible instead of being absorbed by the second.
//
// A 2xx is not evidence of a usable credential. Every field below would build a
// URL that looks right and carries nothing the edge will honour — and an empty
// token is exactly the "non-empty placeholder defeats an is-it-empty gate" shape
// this workspace has been bitten by, except the placeholder is "".
func TestNewDeliveryTicket_RejectsAnUnusableMintResponse(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*deliveryMintResponse)
		wantErr bool
	}{
		{name: "healthy response", mutate: func(*deliveryMintResponse) {}},
		{name: "empty token", mutate: func(r *deliveryMintResponse) { r.Token = "" }, wantErr: true},
		{name: "token carrying query structure", mutate: func(r *deliveryMintResponse) { r.Token = "tok&admin=1" }, wantErr: true},
		{name: "empty param name", mutate: func(r *deliveryMintResponse) { r.Param = "" }, wantErr: true},
		{name: "param name carrying query structure", mutate: func(r *deliveryMintResponse) { r.Param = "mst&public=1" }, wantErr: true},
		{name: "zero lifetime", mutate: func(r *deliveryMintResponse) { r.ExpiresIn = 0 }, wantErr: true},
		{name: "negative lifetime", mutate: func(r *deliveryMintResponse) { r.ExpiresIn = -1 }, wantErr: true},
		{name: "composed prefix is not a prefix", mutate: func(r *deliveryMintResponse) { r.Prefix = strings.TrimSuffix(r.Prefix, "/") }, wantErr: true},
		{name: "composed prefix broader than requested", mutate: func(r *deliveryMintResponse) {
			r.Prefix = "apps/1f0e2c3d-4a5b-6c7d-8e9f-0a1b2c3d4e5f/2a9d8c7b-6e5f-4a3b-2c1d-0e9f8a7b6c5d/_gated/"
			r.URLPrefix = testDeliveryCDNBase + "/" + r.Prefix
		}, wantErr: true},
		{name: "no cdn origin", mutate: func(r *deliveryMintResponse) { r.URLPrefix = "" }, wantErr: true},
		{name: "cdn url is not absolute", mutate: func(r *deliveryMintResponse) { r.URLPrefix = "/" + r.Prefix }, wantErr: true},
		{name: "cdn url does not end in the composed prefix", mutate: func(r *deliveryMintResponse) {
			r.URLPrefix = testDeliveryCDNBase + "/somewhere/else/"
		}, wantErr: true},
		{name: "unparseable expiresAt falls back rather than failing a good mint",
			mutate: func(r *deliveryMintResponse) { r.ExpiresAt = "not a timestamp" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := okMintResponse()
			tc.mutate(&resp)

			ticket, err := newDeliveryTicket(resp, testDeliveryPrefix)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("newDeliveryTicket accepted an unusable response and returned %+v", ticket)
				}
				if got, urlErr := ticket.URL("seg_000001.ts"); urlErr == nil || got != "" {
					t.Fatalf("rejected ticket still produced %q (err=%v)", got, urlErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("newDeliveryTicket: %v", err)
			}
			got, err := ticket.URL("seg_000001.ts")
			if err != nil {
				t.Fatalf("ticket.URL: %v", err)
			}
			if !strings.HasSuffix(got, "?mst="+testDeliveryToken) {
				t.Errorf("url = %q, want the credential attached", got)
			}
			if ticket.ExpiresAt.IsZero() {
				t.Error("ExpiresAt is zero; a caller cannot schedule a re-mint")
			}
		})
	}
}

// The same guarantee at the ticket layer: a zero ticket cannot be talked into
// producing an unsigned URL, so a caller that ignores Delivery's error still
// cannot publish one.
func TestDeliveryTicket_ZeroTicketCannotProduceAURL(t *testing.T) {
	var zero DeliveryTicket
	got, err := zero.URL("seg_000001.ts")
	if err == nil {
		t.Fatalf("zero ticket produced %q, want an error", got)
	}
	if got != "" {
		t.Fatalf("zero ticket returned %q, want no url", got)
	}
}

// ttl is a PROPOSAL and travels as whole seconds. "Unset" must stay
// distinguishable from an explicit 0 on the wire, or the platform's default
// silently replaces a caller's intent.
func TestDeliveryURL_TTLPassthrough(t *testing.T) {
	cases := []struct {
		name    string
		ttl     time.Duration
		want    string // raw JSON value expected for ttlSeconds; "" = field absent
		wantErr bool
	}{
		{name: "one hour", ttl: time.Hour, want: "3600"},
		{name: "the platform floor", ttl: time.Minute, want: "60"},
		{name: "above the platform ceiling is still sent verbatim and clamped there", ttl: 12 * time.Hour, want: "43200"},
		{name: "zero omits the field so the platform default applies", ttl: 0, want: ""},
		{name: "sub-second rounds up to an explicit 1s, never to absent", ttl: 500 * time.Millisecond, want: "1"},
		{name: "fractional rounds up", ttl: 90500 * time.Millisecond, want: "91"},
		{name: "negative is a caller bug, not a default", ttl: -time.Second, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newMintServer(t, func(w http.ResponseWriter, _ *mintRecord) {
				writeMint(w, http.StatusOK, okMintResponse())
			})

			_, err := DeliveryURL(deliveryCtx(), testDeliveryKey, tc.ttl)
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

// An app-scoped context is required: the mint URL is app-scoped and the
// platform re-resolves the app from it.
func TestDeliveryURL_RequiresAnAppScopedContext(t *testing.T) {
	rec := newMintServer(t, func(w http.ResponseWriter, _ *mintRecord) {
		writeMint(w, http.StatusOK, okMintResponse())
	})

	got, err := DeliveryURL(context.Background(), testDeliveryKey, time.Hour)
	if err == nil {
		t.Fatalf("DeliveryURL without an app scope returned %q, want an error", got)
	}
	if got != "" {
		t.Errorf("returned %q, want no url", got)
	}
	if rec.calls != 0 {
		t.Errorf("made %d mint call(s) with no app scope, want 0", rec.calls)
	}
}

func TestSplitDeliveryKey(t *testing.T) {
	cases := []struct {
		name       string
		key        string
		wantPrefix string
		wantName   string
		wantErr    bool
	}{
		{name: "segment under a job", key: testDeliveryKey, wantPrefix: testDeliveryPrefix, wantName: "seg_000042.ts"},
		{name: "object directly under _gated", key: "_gated/probe.txt", wantPrefix: GatedKeyPrefix, wantName: "probe.txt"},
		{name: "ungated key is not deliverable", key: "videos/v1/seg.ts", wantErr: true},
		{name: "_gated as a substring is not the prefix", key: "_gatedx/v1/seg.ts", wantErr: true},
		{name: "prefix, not an object", key: testDeliveryPrefix, wantErr: true},
		{name: "empty", key: "", wantErr: true},
		{name: "absolute", key: "/_gated/v1/seg.ts", wantErr: true},
		{name: "traversal", key: "_gated/../other/seg.ts", wantErr: true},
		{name: "double slash", key: "_gated//v1/seg.ts", wantErr: true},
		{name: "percent-encoding", key: "_gated/v1/seg%2e.ts", wantErr: true},
		// 🔴 The canonical string the platform signs is LF-delimited, and
		// storage.validateKey does NOT reject "\n". A key that smuggles one must
		// die here rather than travel.
		{name: "newline would forge a canonical field", key: "_gated/v1/seg\n.ts", wantErr: true},
		{name: "query separator would smuggle a second credential", key: "_gated/v1/seg.ts?mst=x", wantErr: true},
		{name: "fragment", key: "_gated/v1/seg.ts#x", wantErr: true},
		{name: "space", key: "_gated/v1/seg 1.ts", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefix, name, err := splitDeliveryKey(tc.key)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("splitDeliveryKey(%q) = (%q, %q), want an error", tc.key, prefix, name)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitDeliveryKey(%q): %v", tc.key, err)
			}
			if prefix != tc.wantPrefix || name != tc.wantName {
				t.Errorf("splitDeliveryKey(%q) = (%q, %q), want (%q, %q)", tc.key, prefix, name, tc.wantPrefix, tc.wantName)
			}
		})
	}
}

func TestValidateDeliveryPrefix(t *testing.T) {
	cases := []struct {
		name    string
		prefix  string
		wantErr bool
	}{
		{name: "job prefix", prefix: testDeliveryPrefix},
		{name: "bare _gated is legal here but is a bucket token; scope narrower", prefix: GatedKeyPrefix},
		{name: "empty", prefix: "", wantErr: true},
		{name: "ungated", prefix: "videos/v1/", wantErr: true},
		{name: "no trailing slash", prefix: "_gated/videos/v1", wantErr: true},
		{name: "traversal", prefix: "_gated/../", wantErr: true},
		{name: "double slash", prefix: "_gated//v1/", wantErr: true},
		{name: "newline", prefix: "_gated/v1\n/", wantErr: true},
		{name: "too long for the platform's 512-byte composed ceiling",
			prefix: GatedKeyPrefix + strings.Repeat("a", maxDeliveryPrefixLen) + "/", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDeliveryPrefix(tc.prefix)
			if tc.wantErr && err == nil {
				t.Errorf("validateDeliveryPrefix(%q) = nil, want an error", tc.prefix)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateDeliveryPrefix(%q) = %v, want nil", tc.prefix, err)
			}
		})
	}
}

func TestDeliveryTicket_URLRejectsKeysThatEscapeThePrefix(t *testing.T) {
	newMintServer(t, func(w http.ResponseWriter, _ *mintRecord) {
		writeMint(w, http.StatusOK, okMintResponse())
	})
	ticket, err := Delivery(deliveryCtx(), testDeliveryPrefix, time.Hour)
	if err != nil {
		t.Fatalf("Delivery: %v", err)
	}

	// The token authorises a prefix and the edge checks key.starts_with(p), so a
	// relative key that walks out of it would be a 403 at best. Refuse to build
	// the url rather than emit one that cannot work.
	for _, key := range []string{"../other/seg.ts", "/seg.ts", "", "sub/", "seg.ts?mst=other", "seg\n.ts"} {
		got, err := ticket.URL(key)
		if err == nil {
			t.Errorf("ticket.URL(%q) = %q, want an error", key, got)
		}
		if got != "" {
			t.Errorf("ticket.URL(%q) returned %q, want no url", key, got)
		}
	}
}
