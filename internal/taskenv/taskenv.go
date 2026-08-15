// Package taskenv reads the canonical one-shot task environment.
// It is a leaf package (no internal imports) so any SDK package can import it
// without creating a cycle — mirrors the lambdaenv pattern.
package taskenv

import "os"

const (
	OneShotVar        = "MS_TASK_ONE_SHOT"
	BrokerURLVar      = "MS_TASK_BROKER_URL"
	JobIDVar          = "MS_TASK_JOB_ID"
	AttemptIDVar      = "MS_TASK_ATTEMPT_ID"
	BootstrapTokenVar = "MS_TASK_BOOTSTRAP_TOKEN"
	// ClaimFileVar is set only by the generic runner after it consumes the
	// one-time bootstrap token and writes the broker claim to an owner-only file.
	ClaimFileVar = "MS_TASK_CLAIM_FILE"
)

// IsSet reports whether the process should run exactly one broker attempt.
func IsSet() bool { return os.Getenv(OneShotVar) == "1" }
