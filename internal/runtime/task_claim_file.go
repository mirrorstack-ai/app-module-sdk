package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// claimFileLimit is deliberately smaller than a general HTTP body. A claim
// contains at most the SDK task payload limit plus compact workload/resources.
const claimFileLimit int64 = 512 << 10

// LoadClaimFile reads the generic runner's preclaimed handoff. It rejects
// symlinks, non-regular files, group/world permissions, size ambiguity, and
// trailing JSON so an attacker cannot substitute a less protected source.
func LoadClaimFile(path string) (ClaimResponse, error) {
	if path == "" || !filepath.IsAbs(path) {
		return ClaimResponse{}, errors.New("mirrorstack: MS_TASK_CLAIM_FILE must be an absolute path")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return ClaimResponse{}, fmt.Errorf("mirrorstack: read task claim file: %w", err)
	}
	if !before.Mode().IsRegular() {
		return ClaimResponse{}, errors.New("mirrorstack: task claim file must be a regular file")
	}
	if before.Mode().Perm()&0o077 != 0 {
		return ClaimResponse{}, errors.New("mirrorstack: task claim file must not be accessible by group or others")
	}
	if before.Size() <= 0 || before.Size() > claimFileLimit {
		return ClaimResponse{}, fmt.Errorf("mirrorstack: task claim file size must be between 1 and %d bytes", claimFileLimit)
	}
	f, err := os.Open(path)
	if err != nil {
		return ClaimResponse{}, fmt.Errorf("mirrorstack: open task claim file: %w", err)
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Mode().Perm()&0o077 != 0 {
		return ClaimResponse{}, errors.New("mirrorstack: task claim file changed or is not protected")
	}
	raw, err := io.ReadAll(io.LimitReader(f, claimFileLimit+1))
	if err != nil {
		return ClaimResponse{}, errors.New("mirrorstack: read task claim file failed")
	}
	defer zeroBytes(raw)
	if int64(len(raw)) > claimFileLimit {
		return ClaimResponse{}, fmt.Errorf("mirrorstack: task claim file exceeds %d bytes", claimFileLimit)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var claim ClaimResponse
	if err := dec.Decode(&claim); err != nil {
		return ClaimResponse{}, errors.New("mirrorstack: task claim file contains malformed JSON")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return ClaimResponse{}, errors.New("mirrorstack: task claim file must contain exactly one JSON value")
	}
	if claim.Version != 1 {
		return ClaimResponse{}, errors.New("mirrorstack: task claim file has unsupported version")
	}
	return claim, nil
}
