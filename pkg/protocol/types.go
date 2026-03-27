// Package protocol defines the shared types used across all burst family libraries.
//
// These types form the contract between burst-core and the language libraries
// (adder, Fatou.jl, stet, aji). All language libraries must mirror these
// definitions exactly — field names, JSON tags, and semantics must match.
package protocol

import (
	"encoding/json"
	"fmt"
	"time"
)

// Status constants for SessionStatus.Status.
const (
	StatusInitializing = "initializing"
	StatusRunning      = "running"
	StatusComplete     = "complete"
	StatusFailed       = "failed"
	StatusPartial      = "partial"
)

// SessionStatus is the canonical session state struct.
// All language libraries must define an equivalent struct with identical
// JSON serialization.
type SessionStatus struct {
	SessionID      string    `json:"session_id"`
	Language       string    `json:"language"`
	Status         string    `json:"status"` // initializing|running|complete|failed|partial
	TasksTotal     int       `json:"tasks_total"`
	TasksComplete  int       `json:"tasks_complete"`
	TasksFailed    int       `json:"tasks_failed"`
	WorkersActive  int       `json:"workers_active"`
	ElapsedSeconds float64   `json:"elapsed_seconds"`
	CostActual     float64   `json:"cost_actual"`
	CostEstimate   float64   `json:"cost_estimate_per_hour"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Manifest is written to S3 at sessions/{id}/manifest.json.
// It embeds SessionStatus and adds deployment-time fields.
type Manifest struct {
	SessionStatus
	EnvHash          string `json:"env_hash"`
	LibraryVersion   string `json:"library_version"`
	WorkersRequested int    `json:"workers_requested"`
	WorkersActual    int    `json:"workers_actual"`
	CPU              int    `json:"cpu"`
	MemoryGB         int    `json:"memory_gb"`
	Backend          string `json:"backend"` // fargate|ec2
	Spot             bool   `json:"spot"`
	Region           string `json:"region"`
	ChunkCount       int    `json:"chunk_count"`
	TaskCount        int    `json:"task_count"`
}

// BurstPartialError is returned when some tasks complete and some fail.
// Callers should always check for this type with errors.As when processing results.
type BurstPartialError struct {
	Results      []json.RawMessage // nil entries where task failed
	Errors       []string          // empty string entries where task succeeded
	FailedCount  int
	SuccessCount int
}

func (e *BurstPartialError) Error() string {
	return fmt.Sprintf("burst: %d/%d tasks failed", e.FailedCount, e.FailedCount+e.SuccessCount)
}

// BurstQuotaError is returned when an AWS quota prevents launching the requested
// number of workers. The job continues with ActualWorkers instead.
type BurstQuotaError struct {
	RequestedWorkers int
	ActualWorkers    int
	QuotaName        string
	QuotaValue       float64
}

func (e *BurstQuotaError) Error() string {
	return fmt.Sprintf("burst: quota limited to %d workers (requested %d); quota %q = %.0f vCPUs",
		e.ActualWorkers, e.RequestedWorkers, e.QuotaName, e.QuotaValue)
}

// BurstCostLimitError is returned when the estimated job cost exceeds the
// configured MaxCostPerJob limit. No workers are launched.
type BurstCostLimitError struct {
	Limit          float64
	EstimatedCost  float64
	PartialResults []json.RawMessage
}

func (e *BurstCostLimitError) Error() string {
	return fmt.Sprintf("burst: estimated cost $%.2f exceeds limit $%.2f", e.EstimatedCost, e.Limit)
}

// BurstTimeoutError is returned when the context deadline is exceeded while
// waiting for workers. The session remains in S3 and can be reattached.
type BurstTimeoutError struct {
	SessionID string
	Status    *SessionStatus
}

func (e *BurstTimeoutError) Error() string {
	if e.Status != nil {
		return fmt.Sprintf("burst: session %s timed out (%d/%d tasks complete)",
			e.SessionID, e.Status.TasksComplete, e.Status.TasksTotal)
	}
	return fmt.Sprintf("burst: session %s timed out", e.SessionID)
}

// BurstSetupError is returned when AWS resource provisioning fails.
// Step names the setup step that failed (e.g. "create S3 bucket").
// Remediation provides a human-readable fix hint.
type BurstSetupError struct {
	Step        string
	Cause       string
	Remediation string
}

func (e *BurstSetupError) Error() string {
	if e.Remediation != "" {
		return fmt.Sprintf("burst setup failed at %q: %s — %s", e.Step, e.Cause, e.Remediation)
	}
	return fmt.Sprintf("burst setup failed at %q: %s", e.Step, e.Cause)
}
