// Package cost provides Fargate cost estimation and the canonical display format
// shared by all burst family libraries.
//
// All five language libraries must emit identical console output for cost lines.
// The format strings in this package are the source of truth.
package cost

import (
	"fmt"
	"time"
)

// Fargate on-demand pricing (us-east-1, 2026).
// These constants are used for estimation only; actual cost is queried from
// AWS Cost Explorer after job completion.
const (
	FargatevCPUPerHour float64 = 0.04048
	FargateGBPerHour   float64 = 0.004445
)

// EstimateCost returns the estimated USD cost for a burst job.
func EstimateCost(cpu, memoryGB, workers int, hours float64) float64 {
	vcpuCost := float64(cpu) * float64(workers) * hours * FargatevCPUPerHour
	memCost := float64(memoryGB) * float64(workers) * hours * FargateGBPerHour
	return vcpuCost + memCost
}

// EstimateCostPerHour returns the estimated USD cost per hour for a running cluster.
func EstimateCostPerHour(cpu, memoryGB, workers int) float64 {
	return EstimateCost(cpu, memoryGB, workers, 1.0)
}

// StartLine returns the first console line emitted when a burst job begins.
//
//	🚀 Starting burst cluster with 50 workers
func StartLine(workers int) string {
	return fmt.Sprintf("🚀 Starting burst cluster with %d workers", workers)
}

// CostEstimateLine returns the cost estimate line.
//
//	💰 Estimated cost: ~$2.80/hour
func CostEstimateLine(perHour float64) string {
	return fmt.Sprintf("💰 Estimated cost: ~$%.2f/hour", perHour)
}

// ProcessingLine returns the items/workers summary line.
//
//	📊 Processing 1000 items with 50 workers
func ProcessingLine(total, workers int) string {
	return fmt.Sprintf("📊 Processing %d items with %d workers", total, workers)
}

// ChunksLine returns the chunk summary line.
//
//	📦 Created 50 chunks (avg 20 items per chunk)
func ChunksLine(chunks, avgItems int) string {
	return fmt.Sprintf("📦 Created %d chunks (avg %d items per chunk)", chunks, avgItems)
}

// SubmittingLine is emitted just before tasks are submitted.
//
//	🚀 Submitting tasks...
const SubmittingLine = "🚀 Submitting tasks..."

// SubmittedLine returns the confirmation line after task submission.
//
//	✓ Submitted 50 tasks
func SubmittedLine(n int) string {
	return fmt.Sprintf("✓ Submitted %d tasks", n)
}

// ProgressLine returns the polling progress line.
//
//	⏳ Progress: 42/100 tasks (15.3s elapsed)
func ProgressLine(done, total int, elapsed time.Duration) string {
	return fmt.Sprintf("⏳ Progress: %d/%d tasks (%s elapsed)", done, total, formatDuration(elapsed))
}

// CompletedLine returns the completion line.
//
//	✓ Completed in 45.2s
func CompletedLine(elapsed time.Duration) string {
	return fmt.Sprintf("✓ Completed in %s", formatDuration(elapsed))
}

// ActualCostLine returns the actual cost line printed at job completion.
//
//	💰 Actual cost: $1.23
func ActualCostLine(actual float64) string {
	return fmt.Sprintf("💰 Actual cost: $%.2f", actual)
}

// QuotaWarningLines returns the two-line quota warning block.
//
//	⚠ Requested 100 workers (200 vCPUs) but quota allows 16 workers (32 vCPUs)
//	⚠ Using 16 workers instead. Request quota increase: https://console.aws.amazon.com/servicequotas/
func QuotaWarningLines(requested, actual int, requestedVCPU, actualVCPU float64) [2]string {
	return [2]string{
		fmt.Sprintf("⚠ Requested %d workers (%.0f vCPUs) but quota allows %d workers (%.0f vCPUs)",
			requested, requestedVCPU, actual, actualVCPU),
		fmt.Sprintf("⚠ Using %d workers instead. Request quota increase: https://console.aws.amazon.com/servicequotas/",
			actual),
	}
}

// CostAlertLine returns the cost alert warning line.
//
//	⚠ Estimated cost exceeds alert threshold of $5.00
func CostAlertLine(threshold float64) string {
	return fmt.Sprintf("⚠ Estimated cost exceeds alert threshold of $%.2f", threshold)
}

// formatDuration formats a duration for display, e.g. "15.3s", "2m 4.5s".
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	mins := int(d.Minutes())
	secs := d.Seconds() - float64(mins)*60
	return fmt.Sprintf("%dm %.1fs", mins, secs)
}
