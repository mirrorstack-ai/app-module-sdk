package sdktest

import (
	"errors"
	"fmt"

	"github.com/mirrorstack-ai/app-module-sdk/internal/contributions"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
	"github.com/mirrorstack-ai/app-module-sdk/system"
)

// ErrContributionSlotNotProvided means the host manifest does not declare the
// slot targeted by an outbound contribution.
var ErrContributionSlotNotProvided = errors.New("sdktest: contribution slot is not provided")

// ErrNoContributionsToHost means the contributor manifest does not target the
// supplied host by its manifest ID, slug, or an explicitly supplied reference.
var ErrNoContributionsToHost = errors.New("sdktest: contributor does not target host")

// OutboundContribution is one contributor-side manifest declaration.
type OutboundContribution = registry.OutboundContribution

// ValidateContributionPayload checks one contributor manifest entry against
// the exact JSON Schema advertised by its host manifest.
func ValidateContributionPayload(host system.ManifestPayload, outbound OutboundContribution) error {
	for _, slot := range host.Provides {
		if slot.Key != outbound.Slot {
			continue
		}
		validator, err := contributions.CompilePayloadValidator(slot.Payload)
		if err != nil {
			return fmt.Errorf("sdktest: compile host slot %q schema: %w", outbound.Slot, err)
		}
		if err := validator(outbound.Payload); err != nil {
			return fmt.Errorf("sdktest: contribution payload for slot %q: %w", outbound.Slot, err)
		}
		return nil
	}
	return fmt.Errorf("%w: %s", ErrContributionSlotNotProvided, outbound.Slot)
}

// ValidateContributionsToHost checks every entry in contributor that targets
// host. Exact catalog references such as "@owner/user-core" belong in
// hostReferences because a manifest's slug cannot authenticate or reconstruct
// its catalog owner. It is the compatibility gate for unchanged module
// canaries.
func ValidateContributionsToHost(host, contributor system.ManifestPayload, hostReferences ...string) error {
	targets := make(map[string]struct{}, len(hostReferences)+2)
	for _, target := range append([]string{host.ID, host.Slug}, hostReferences...) {
		if target != "" {
			targets[target] = struct{}{}
		}
	}
	matched := false
	var validationErr error
	for _, outbound := range contributor.ContributesTo {
		if _, accepted := targets[outbound.Host]; !accepted {
			continue
		}
		matched = true
		if err := ValidateContributionPayload(host, outbound); err != nil {
			validationErr = errors.Join(validationErr, fmt.Errorf("%s: %w", contributor.Slug, err))
		}
	}
	if !matched {
		return fmt.Errorf("%w: %s", ErrNoContributionsToHost, host.Slug)
	}
	return validationErr
}
