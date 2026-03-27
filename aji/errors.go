package aji

import "github.com/scttfrdmn/burst-core/pkg/protocol"

// aji error types are type aliases for the canonical protocol error types.
// Users can use errors.As(err, &aji.BurstPartialError{}) without importing protocol.
//
// See ARCHITECTURE.md for the error type contract shared across all burst family libraries.
type (
	// BurstPartialError is returned when some tasks complete and some fail.
	// Results contains nil/null entries where tasks failed; Errors contains empty strings
	// where tasks succeeded.
	BurstPartialError = protocol.BurstPartialError

	// BurstQuotaError is returned when an AWS quota prevents launching all requested workers.
	// The job continues with ActualWorkers instead.
	BurstQuotaError = protocol.BurstQuotaError

	// BurstCostLimitError is returned when the estimated job cost exceeds MaxCostPerJob.
	// No workers are launched.
	BurstCostLimitError = protocol.BurstCostLimitError

	// BurstTimeoutError is returned when the context deadline is exceeded while waiting
	// for workers. The session remains in S3 and can be reattached with Attach().
	BurstTimeoutError = protocol.BurstTimeoutError

	// BurstSetupError is returned when AWS resource provisioning fails or the config
	// file is missing. Check Step, Cause, and Remediation for actionable guidance.
	BurstSetupError = protocol.BurstSetupError
)
