package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/mirrorstack-ai/app-module-sdk/system"
)

const (
	sdkToolModeEnv               = "MS_SDK_TOOL_MODE"
	releaseManifestToolMode      = "release-manifest-v1"
	releaseManifestToolProtocol  = "mirrorstack.release-manifest/v1"
	releaseManifestInputMaxBytes = 4 * 1024
)

var canonicalSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type releaseManifestToolInput struct {
	SourceSHA256 string
}

type releaseManifestToolEnvelope struct {
	Protocol       string `json:"protocol"`
	SourceSHA256   string `json:"source_sha256"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ManifestBase64 string `json:"manifest_base64"`
}

// runSDKToolMode handles reserved SDK process modes before any runtime mode is
// selected. A non-empty unknown mode is handled as an error rather than falling
// through to Lambda, task, migration, or listener startup.
func (m *Module) runSDKToolMode(mode string, in io.Reader, out io.Writer) (bool, error) {
	switch mode {
	case "":
		return false, nil
	case releaseManifestToolMode:
		return true, m.runReleaseManifestTool(in, out)
	default:
		return true, fmt.Errorf("mirrorstack: unsupported %s %q", sdkToolModeEnv, mode)
	}
}

func (m *Module) runReleaseManifestTool(in io.Reader, out io.Writer) error {
	input, err := decodeReleaseManifestToolInput(in)
	if err != nil {
		return fmt.Errorf("mirrorstack: release manifest tool input: %w", err)
	}

	document, err := system.BuildManifest(
		m.config.ID,
		m.config.Slug,
		m.config.Name,
		m.config.Icon,
		m.config.Tags,
		m.config.SQL,
		m.config.Versions,
		m.registry,
		m.contribReg,
		m.config.Client,
	)
	if err != nil {
		return fmt.Errorf("mirrorstack: release manifest tool build: %w", err)
	}

	envelope := releaseManifestToolEnvelope{
		Protocol:       releaseManifestToolProtocol,
		SourceSHA256:   input.SourceSHA256,
		ManifestSHA256: document.SHA256,
		ManifestBase64: base64.StdEncoding.EncodeToString(document.Body),
	}
	if err := json.NewEncoder(out).Encode(envelope); err != nil {
		return fmt.Errorf("mirrorstack: release manifest tool output: %w", err)
	}
	return nil
}

func decodeReleaseManifestToolInput(in io.Reader) (releaseManifestToolInput, error) {
	raw, err := io.ReadAll(io.LimitReader(in, releaseManifestInputMaxBytes+1))
	if err != nil {
		return releaseManifestToolInput{}, fmt.Errorf("read: %w", err)
	}
	if len(raw) > releaseManifestInputMaxBytes {
		return releaseManifestToolInput{}, fmt.Errorf("exceeds %d-byte limit", releaseManifestInputMaxBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil {
		return releaseManifestToolInput{}, fmt.Errorf("decode object: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return releaseManifestToolInput{}, errors.New("must be one JSON object")
	}

	var input releaseManifestToolInput
	seenSource := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return releaseManifestToolInput{}, fmt.Errorf("decode field: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return releaseManifestToolInput{}, errors.New("object field name must be a string")
		}
		if key != "source_sha256" {
			return releaseManifestToolInput{}, fmt.Errorf("unknown field %q", key)
		}
		if seenSource {
			return releaseManifestToolInput{}, errors.New("duplicate field \"source_sha256\"")
		}
		seenSource = true
		if err := decoder.Decode(&input.SourceSHA256); err != nil {
			return releaseManifestToolInput{}, fmt.Errorf("source_sha256 must be a string: %w", err)
		}
	}

	closing, err := decoder.Token()
	if err != nil {
		return releaseManifestToolInput{}, fmt.Errorf("close object: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return releaseManifestToolInput{}, errors.New("input object is not closed")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return releaseManifestToolInput{}, fmt.Errorf("trailing input: %w", err)
		}
		return releaseManifestToolInput{}, errors.New("trailing input after object")
	}
	if !seenSource {
		return releaseManifestToolInput{}, errors.New("source_sha256 is required")
	}
	if !canonicalSHA256Pattern.MatchString(input.SourceSHA256) {
		return releaseManifestToolInput{}, errors.New("source_sha256 must be exactly 64 lowercase hexadecimal characters")
	}
	return input, nil
}
