package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mirrorstack-ai/app-module-sdk/internal/contributions"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
	"github.com/mirrorstack-ai/app-module-sdk/meter"
	"github.com/mirrorstack-ai/app-module-sdk/roles"
	"github.com/mirrorstack-ai/app-module-sdk/system"
)

const releaseManifestTestSourceSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestDecodeReleaseManifestToolInput(t *testing.T) {
	t.Parallel()

	valid := `{"source_sha256":"` + releaseManifestTestSourceSHA + `"}`
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "array", input: `[]`},
		{name: "missing", input: `{}`},
		{name: "unknown field", input: `{"source_sha256":"` + releaseManifestTestSourceSHA + `","extra":true}`},
		{name: "duplicate field", input: `{"source_sha256":"` + releaseManifestTestSourceSHA + `","source_sha256":"` + releaseManifestTestSourceSHA + `"}`},
		{name: "wrong type", input: `{"source_sha256":7}`},
		{name: "uppercase", input: `{"source_sha256":"` + strings.ToUpper(releaseManifestTestSourceSHA) + `"}`},
		{name: "too short", input: `{"source_sha256":"abc"}`},
		{name: "non hexadecimal", input: `{"source_sha256":"` + strings.Repeat("g", 64) + `"}`},
		{name: "second value", input: valid + `{}`},
		{name: "trailing scalar", input: valid + ` true`},
		{name: "malformed", input: `{"source_sha256":`},
		{name: "over limit", input: `{"source_sha256":"` + strings.Repeat("a", releaseManifestInputMaxBytes) + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeReleaseManifestToolInput(strings.NewReader(test.input)); err == nil {
				t.Fatal("decodeReleaseManifestToolInput() = nil error, want rejection")
			}
		})
	}

	got, err := decodeReleaseManifestToolInput(strings.NewReader(" \n" + valid + "\t"))
	if err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	if got.SourceSHA256 != releaseManifestTestSourceSHA {
		t.Errorf("source SHA = %q, want %q", got.SourceSHA256, releaseManifestTestSourceSHA)
	}
}

func TestRunSDKToolMode_FailsClosed(t *testing.T) {
	t.Parallel()

	module, err := newReleaseManifestToolFixture()
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"source_sha256":"` + releaseManifestTestSourceSHA + `"}`

	t.Run("unset preserves ordinary startup", func(t *testing.T) {
		var output bytes.Buffer
		handled, err := module.runSDKToolMode("", strings.NewReader(valid), &output)
		if handled || err != nil || output.Len() != 0 {
			t.Fatalf("unset mode = (%v, %v, %q), want (false, nil, empty)", handled, err, output.String())
		}
	})

	t.Run("unknown mode does not fall through", func(t *testing.T) {
		var output bytes.Buffer
		handled, err := module.runSDKToolMode("release-manifest-v2", strings.NewReader(valid), &output)
		if !handled || err == nil {
			t.Fatalf("unknown mode = (%v, %v), want handled error", handled, err)
		}
		if output.Len() != 0 {
			t.Fatalf("unknown mode wrote stdout: %q", output.String())
		}
	})

	t.Run("malformed input writes no envelope", func(t *testing.T) {
		var output bytes.Buffer
		handled, err := module.runSDKToolMode(releaseManifestToolMode, strings.NewReader(`{}`), &output)
		if !handled || err == nil {
			t.Fatalf("malformed input = (%v, %v), want handled error", handled, err)
		}
		if output.Len() != 0 {
			t.Fatalf("malformed input wrote stdout: %q", output.String())
		}
	})

	t.Run("output failure is returned", func(t *testing.T) {
		handled, err := module.runSDKToolMode(releaseManifestToolMode, strings.NewReader(valid), failingWriter{})
		if !handled || err == nil || !strings.Contains(err.Error(), "output") {
			t.Fatalf("output failure = (%v, %v), want handled output error", handled, err)
		}
	})
}

func TestReleaseManifestTool_EnvelopeCarriesExactServedBytes(t *testing.T) {
	t.Parallel()

	module, err := newReleaseManifestToolFixture()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := module.runReleaseManifestTool(
		strings.NewReader(`{"source_sha256":"`+releaseManifestTestSourceSHA+`"}`),
		&output,
	); err != nil {
		t.Fatalf("runReleaseManifestTool: %v", err)
	}
	if bytes.Count(output.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("stdout must be exactly one envelope line, got %q", output.String())
	}

	var envelope releaseManifestToolEnvelope
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	manifestBytes, err := base64.StdEncoding.DecodeString(envelope.ManifestBase64)
	if err != nil {
		t.Fatalf("decode manifest_base64: %v", err)
	}

	handler := system.ManifestHandlerWithClient(
		module.config.ID,
		module.config.Slug,
		module.config.Name,
		module.config.Icon,
		module.config.Tags,
		module.config.SQL,
		module.config.Versions,
		module.registry,
		module.contribReg,
		module.config.Client,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/__mirrorstack/platform/manifest", nil))
	if !bytes.Equal(manifestBytes, recorder.Body.Bytes()) {
		t.Fatalf("decoded manifest_base64 and served body differ\ntool: %s\nHTTP: %s", manifestBytes, recorder.Body.Bytes())
	}
	if envelope.ManifestSHA256 != recorder.Header().Get("X-MS-Manifest-Hash") {
		t.Errorf("tool hash = %q, served hash = %q", envelope.ManifestSHA256, recorder.Header().Get("X-MS-Manifest-Hash"))
	}
}

// TestReleaseManifestTool_StartsBeforeEveryRuntimePlane runs the real Start
// entrypoint in fresh processes because Lambda and one-shot detection is fixed
// at process initialization. Every child carries a trap that would fail or
// block if Start reached the selected runtime path. Identical successful bytes
// therefore prove the tool branch precedes Lambda handoff, task claim, dev
// migration/database setup, and HTTP listening.
func TestReleaseManifestTool_StartsBeforeEveryRuntimePlane(t *testing.T) {
	if helperMode := os.Getenv("GO_WANT_RELEASE_MANIFEST_TOOL_HELPER"); helperMode != "" {
		runReleaseManifestToolHelper()
		return
	}

	t.Parallel()
	tests := []struct {
		name string
		env  string
	}{
		{name: "lambda", env: "AWS_LAMBDA_FUNCTION_NAME=release-manifest-trap"},
		{name: "one-shot task", env: "MS_TASK_ONE_SHOT=1"},
		{name: "dev migration and database", env: "MS_LOCAL_DB_URL=not-a-postgres-url"},
		{name: "HTTP listener", env: "PORT=not-a-port"},
	}

	var firstOutput []byte
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReleaseManifestTool_StartsBeforeEveryRuntimePlane$")
			command.Env = []string{
				"GO_WANT_RELEASE_MANIFEST_TOOL_HELPER=1",
				"GO_WANT_RELEASE_MANIFEST_DECLARATION_LOG=1",
				sdkToolModeEnv + "=" + releaseManifestToolMode,
				test.env,
			}
			command.Stdin = strings.NewReader(`{"source_sha256":"` + releaseManifestTestSourceSHA + `"}`)
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			err := command.Run()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("tool process reached a blocking runtime path; stderr: %s", stderr.String())
			}
			if err != nil {
				t.Fatalf("tool process: %v\nstderr: %s\nstdout: %s", err, stderr.String(), stdout.String())
			}
			if strings.Contains(stderr.String(), releaseManifestToolProtocol) {
				t.Fatalf("tool envelope leaked onto stderr: %q", stderr.String())
			}
			if !strings.Contains(stderr.String(), "release-manifest declaration diagnostic") {
				t.Fatalf("structured declaration log did not stay on stderr: %q", stderr.String())
			}
			if bytes.Count(stdout.Bytes(), []byte("\n")) != 1 || !bytes.HasSuffix(stdout.Bytes(), []byte("\n")) {
				t.Fatalf("stdout must be exactly one JSON line, got %q", stdout.String())
			}
			assertReleaseManifestEnvelope(t, stdout.Bytes())
			if firstOutput == nil {
				firstOutput = bytes.Clone(stdout.Bytes())
			} else if !bytes.Equal(stdout.Bytes(), firstOutput) {
				t.Fatalf("tool output changed across runtime planes\nfirst: %s\ncurrent: %s", firstOutput, stdout.Bytes())
			}
		})
	}
}

func TestReleaseManifestTool_UnknownModeFailsBeforeEveryRuntimePlane(t *testing.T) {
	if os.Getenv("GO_WANT_UNKNOWN_SDK_TOOL_HELPER") != "" {
		runReleaseManifestToolHelper()
		return
	}

	t.Parallel()
	tests := []struct {
		name string
		env  string
	}{
		{name: "lambda", env: "AWS_LAMBDA_FUNCTION_NAME=unknown-mode-trap"},
		{name: "one-shot task", env: "MS_TASK_ONE_SHOT=1"},
		{name: "dev migration and database", env: "MS_LOCAL_DB_URL=not-a-postgres-url"},
		{name: "HTTP listener", env: "PORT=not-a-port"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReleaseManifestTool_UnknownModeFailsBeforeEveryRuntimePlane$")
			command.Env = []string{
				"GO_WANT_UNKNOWN_SDK_TOOL_HELPER=1",
				"GO_WANT_RELEASE_MANIFEST_DECLARATION_LOG=1",
				sdkToolModeEnv + "=release-manifest-v2",
				test.env,
			}
			command.Stdin = strings.NewReader(`{"source_sha256":"` + releaseManifestTestSourceSHA + `"}`)
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			err := command.Run()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("unknown mode reached a blocking runtime path; stderr: %s", stderr.String())
			}
			if err == nil {
				t.Fatal("unknown mode process succeeded, want non-zero exit")
			}
			if stdout.Len() != 0 {
				t.Fatalf("unknown mode wrote stdout: %q", stdout.String())
			}
			if !strings.Contains(stderr.String(), "release-manifest declaration diagnostic") {
				t.Fatalf("unknown mode declaration log did not stay on stderr: %q", stderr.String())
			}
			if !strings.Contains(stderr.String(), `unsupported MS_SDK_TOOL_MODE "release-manifest-v2"`) {
				t.Fatalf("unknown mode did not fail at SDK tool boundary; stderr: %q", stderr.String())
			}
		})
	}
}

func TestReleaseManifestTool_MigrationDiagnosticsStayOnStderr(t *testing.T) {
	if os.Getenv("GO_WANT_RELEASE_MANIFEST_DIAGNOSTIC_HELPER") != "" {
		runReleaseManifestToolHelper()
		return
	}

	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReleaseManifestTool_MigrationDiagnosticsStayOnStderr$")
	command.Env = []string{
		"GO_WANT_RELEASE_MANIFEST_DIAGNOSTIC_HELPER=1",
		"GO_WANT_RELEASE_MANIFEST_BAD_SQL=1",
		"GO_WANT_RELEASE_MANIFEST_DECLARATION_LOG=1",
		sdkToolModeEnv + "=" + releaseManifestToolMode,
		"PORT=not-a-port",
	}
	command.Stdin = strings.NewReader(`{"source_sha256":"` + releaseManifestTestSourceSHA + `"}`)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("tool process: %v\nstderr: %s\nstdout: %s", err, stderr.String(), stdout.String())
	}
	if bytes.Count(stdout.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("stdout must remain exactly one envelope line, got %q", stdout.String())
	}
	for _, diagnostic := range []string{
		"manifest app migration version unavailable",
		"manifest module migration version unavailable",
	} {
		if !strings.Contains(stderr.String(), diagnostic) {
			t.Errorf("stderr missing %q: %s", diagnostic, stderr.String())
		}
		if strings.Contains(stdout.String(), diagnostic) {
			t.Errorf("stdout contains diagnostic %q: %s", diagnostic, stdout.String())
		}
	}
	if strings.Contains(stderr.String(), "private-release-path") {
		t.Fatalf("sanitized migration diagnostic leaked filesystem detail: %s", stderr.String())
	}

	var envelope releaseManifestToolEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	manifestBytes, err := base64.StdEncoding.DecodeString(envelope.ManifestBase64)
	if err != nil {
		t.Fatalf("decode manifest_base64: %v", err)
	}
	sum := sha256.Sum256(manifestBytes)
	if envelope.ManifestSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("diagnostic path envelope hash does not match exact manifest bytes")
	}
	var manifest system.ManifestPayload
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Migration != (system.MigrationVersions{}) {
		t.Errorf("unavailable migration FS should preserve ordinary empty-version behavior, got %+v", manifest.Migration)
	}
}

func runReleaseManifestToolHelper() {
	module, err := newReleaseManifestToolFixture()
	if err == nil && os.Getenv("GO_WANT_RELEASE_MANIFEST_BAD_SQL") != "" {
		module.config.SQL = failingReleaseManifestFS{}
	}
	if err == nil {
		err = module.Start()
	}
	if err != nil {
		log.Print(err)
		os.Exit(2)
	}
	os.Exit(0)
}

func newReleaseManifestToolFixture() (*Module, error) {
	sqlFS := fstest.MapFS{
		"sql/app/0003_users.up.sql":     &fstest.MapFile{Data: []byte("SELECT 1;")},
		"sql/module/0007_outbox.up.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
	}
	module, err := New(Config{
		ID:          "release_fixture",
		Slug:        "release-fixture",
		Name:        "Release Fixture",
		Icon:        "verified",
		Description: "Complete release manifest fixture.",
		Tags:        []string{"Test", "Release"},
		SQL:         sqlFS,
		Versions: map[string]system.MigrationVersions{
			"v1.0.0": {App: "0003", Module: "0007"},
		},
		WebDir: "web/dist",
		Client: &system.ClientSpec{Dir: "client", OutputDir: "dist"},
	})
	if err != nil {
		return nil, err
	}
	if os.Getenv("GO_WANT_RELEASE_MANIFEST_DECLARATION_LOG") != "" {
		slog.Info("release-manifest declaration diagnostic")
	}

	module.Platform(func(router chi.Router) {
		router.Get("/users", http.NotFound)
	})
	module.Public(func(router chi.Router) {
		router.Get("/profile", http.NotFound)
	})
	module.Internal(func(router chi.Router) {
		router.Post("/sync", http.NotFound)
	})
	module.RegisterPermission("users.read", PermissionOpts{DefaultRole: roles.Viewer()})
	module.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{{Route: "/", Title: "Users", Export: "mountUsers"}},
		Components:   []UIComponent{{Name: "UserCard", Export: "UserCard"}},
	})
	module.DependsOn("oauth-core@^1.0.0", func(need *Need) { need.Table("identities") })
	module.Emits("user.updated")
	module.OnEvent("oauth.user.deleted", http.NotFound)
	module.Cron("cleanup", "0 3 * * *", http.NotFound)
	module.OnTask("reconcile", func(context.Context, json.RawMessage) error { return nil })
	module.ExposeTable("users")
	module.RequireStorage()
	module.Meter("users.active", meter.Gauge, meter.BySubject, meter.Unit("user"))
	module.AbsorbInfra()
	module.ProvideSlot(contributions.NewSlot(
		"user-detail-blocks",
		"fixture.UserDetailBlock",
		json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`),
		nil,
	))
	module.ContributesTo("host-core@^1.0.0", "quick-actions", map[string]string{"label": "Open"})
	return module, nil
}

func assertReleaseManifestEnvelope(t *testing.T, output []byte) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(output))
	var envelope releaseManifestToolEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout contains a second JSON value: %v", err)
	}
	if envelope.Protocol != releaseManifestToolProtocol {
		t.Errorf("protocol = %q, want %q", envelope.Protocol, releaseManifestToolProtocol)
	}
	if envelope.SourceSHA256 != releaseManifestTestSourceSHA {
		t.Errorf("source_sha256 = %q, want %q", envelope.SourceSHA256, releaseManifestTestSourceSHA)
	}

	manifestBytes, err := base64.StdEncoding.DecodeString(envelope.ManifestBase64)
	if err != nil {
		t.Fatalf("manifest_base64 is not standard padded base64: %v", err)
	}
	sum := sha256.Sum256(manifestBytes)
	if want := hex.EncodeToString(sum[:]); envelope.ManifestSHA256 != want {
		t.Errorf("manifest_sha256 = %q, want exact body hash %q", envelope.ManifestSHA256, want)
	}
	if !bytes.HasSuffix(manifestBytes, []byte("\n")) {
		t.Fatal("decoded canonical manifest is missing json.Encoder trailing newline")
	}

	var manifest system.ManifestPayload
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.ID != "release_fixture" || manifest.Slug != "release-fixture" {
		t.Errorf("identity incomplete: id=%q slug=%q", manifest.ID, manifest.Slug)
	}
	if manifest.Migration != (system.MigrationVersions{App: "0003", Module: "0007"}) {
		t.Errorf("migration = %+v, want app 0003/module 0007", manifest.Migration)
	}
	if manifest.Versions["v1.0.0"] != (system.MigrationVersions{App: "0003", Module: "0007"}) {
		t.Errorf("version declaration missing: %+v", manifest.Versions)
	}
	if manifest.UI == nil || len(manifest.UI.DefaultPages) != 1 || len(manifest.UI.Components) != 1 {
		t.Errorf("UI declaration incomplete: %+v", manifest.UI)
	}
	if manifest.Client == nil || manifest.Client.Dir != "client" || manifest.Client.OutputDir != "dist" {
		t.Errorf("client declaration incomplete: %+v", manifest.Client)
	}
	if !manifest.Resources.Storage || len(manifest.Metrics) != 1 || !manifest.AbsorbInfra {
		t.Errorf("resource declarations incomplete: resources=%+v metrics=%+v absorb=%v", manifest.Resources, manifest.Metrics, manifest.AbsorbInfra)
	}
	for _, scope := range registry.AllScopes() {
		if len(manifest.Routes[scope]) == 0 {
			t.Errorf("routes.%s is empty; complete declarations were not preserved", scope)
		}
	}
	if len(manifest.Dependencies) != 1 || len(manifest.Schedules) != 1 || len(manifest.Tasks) != 1 || len(manifest.Permissions) != 1 {
		t.Errorf("declarations incomplete: dependencies=%d schedules=%d tasks=%d permissions=%d", len(manifest.Dependencies), len(manifest.Schedules), len(manifest.Tasks), len(manifest.Permissions))
	}
	if len(manifest.Events.Emits) != 1 || len(manifest.Events.Subscribes) != 1 {
		t.Errorf("event declarations incomplete: emits=%v subscribes=%v", manifest.Events.Emits, manifest.Events.Subscribes)
	}
	if len(manifest.Provides) != 1 || len(manifest.ContributesTo) != 1 || len(manifest.Exposes.Tables) != 1 {
		t.Errorf("extension declarations incomplete: provides=%d contributes=%d exposes=%d", len(manifest.Provides), len(manifest.ContributesTo), len(manifest.Exposes.Tables))
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

type failingReleaseManifestFS struct{}

func (failingReleaseManifestFS) Open(name string) (iofs.File, error) {
	return nil, fmt.Errorf("private-release-path/%s is unavailable", name)
}
