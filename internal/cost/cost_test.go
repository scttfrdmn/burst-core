package cost_test

import (
	"testing"
	"time"

	"github.com/scttfrdmn/burst-core/internal/cost"
)

func TestEstimateCost(t *testing.T) {
	// 10 workers, 2 vCPU, 4 GB, 1 hour
	// = 10 * 2 * 1 * 0.04048 + 10 * 4 * 1 * 0.004445
	// = 0.8096 + 0.1778 = 0.9874
	got := cost.EstimateCost(2, 4, 10, 1.0)
	want := 10*2*1.0*cost.FargatevCPUPerHour + 10*4*1.0*cost.FargateGBPerHour
	if got != want {
		t.Errorf("EstimateCost(2, 4, 10, 1.0) = %.6f, want %.6f", got, want)
	}
}

func TestEstimateCostZeroHours(t *testing.T) {
	got := cost.EstimateCost(4, 8, 50, 0)
	if got != 0 {
		t.Errorf("EstimateCost with 0 hours = %v, want 0", got)
	}
}

func TestEstimateCostPerHour(t *testing.T) {
	got := cost.EstimateCostPerHour(2, 4, 10)
	want := cost.EstimateCost(2, 4, 10, 1.0)
	if got != want {
		t.Errorf("EstimateCostPerHour != EstimateCost(..., 1.0): %v vs %v", got, want)
	}
}

// Canonical output lines — tests assert exact string match against ARCHITECTURE.md format.

func TestStartLine(t *testing.T) {
	got := cost.StartLine(50)
	want := "🚀 Starting burst cluster with 50 workers"
	if got != want {
		t.Errorf("StartLine(50) = %q, want %q", got, want)
	}
}

func TestCostEstimateLine(t *testing.T) {
	got := cost.CostEstimateLine(2.80)
	want := "💰 Estimated cost: ~$2.80/hour"
	if got != want {
		t.Errorf("CostEstimateLine(2.80) = %q, want %q", got, want)
	}
}

func TestProcessingLine(t *testing.T) {
	got := cost.ProcessingLine(1000, 50)
	want := "📊 Processing 1000 items with 50 workers"
	if got != want {
		t.Errorf("ProcessingLine(1000, 50) = %q, want %q", got, want)
	}
}

func TestChunksLine(t *testing.T) {
	got := cost.ChunksLine(50, 20)
	want := "📦 Created 50 chunks (avg 20 items per chunk)"
	if got != want {
		t.Errorf("ChunksLine(50, 20) = %q, want %q", got, want)
	}
}

func TestSubmittedLine(t *testing.T) {
	got := cost.SubmittedLine(50)
	want := "✓ Submitted 50 tasks"
	if got != want {
		t.Errorf("SubmittedLine(50) = %q, want %q", got, want)
	}
}

func TestProgressLine(t *testing.T) {
	got := cost.ProgressLine(42, 100, 15300*time.Millisecond)
	want := "⏳ Progress: 42/100 tasks (15.3s elapsed)"
	if got != want {
		t.Errorf("ProgressLine = %q, want %q", got, want)
	}
}

func TestProgressLineMinutes(t *testing.T) {
	got := cost.ProgressLine(80, 100, 2*time.Minute+4500*time.Millisecond)
	want := "⏳ Progress: 80/100 tasks (2m 4.5s elapsed)"
	if got != want {
		t.Errorf("ProgressLine (minutes) = %q, want %q", got, want)
	}
}

func TestCompletedLine(t *testing.T) {
	got := cost.CompletedLine(45200 * time.Millisecond)
	want := "✓ Completed in 45.2s"
	if got != want {
		t.Errorf("CompletedLine = %q, want %q", got, want)
	}
}

func TestActualCostLine(t *testing.T) {
	got := cost.ActualCostLine(1.23)
	want := "💰 Actual cost: $1.23"
	if got != want {
		t.Errorf("ActualCostLine(1.23) = %q, want %q", got, want)
	}
}

func TestQuotaWarningLines(t *testing.T) {
	lines := cost.QuotaWarningLines(100, 16, 200, 32)
	want0 := "⚠ Requested 100 workers (200 vCPUs) but quota allows 16 workers (32 vCPUs)"
	want1 := "⚠ Using 16 workers instead. Request quota increase: https://console.aws.amazon.com/servicequotas/"
	if lines[0] != want0 {
		t.Errorf("QuotaWarningLines[0] = %q, want %q", lines[0], want0)
	}
	if lines[1] != want1 {
		t.Errorf("QuotaWarningLines[1] = %q, want %q", lines[1], want1)
	}
}

func TestCostAlertLine(t *testing.T) {
	got := cost.CostAlertLine(5.00)
	want := "⚠ Estimated cost exceeds alert threshold of $5.00"
	if got != want {
		t.Errorf("CostAlertLine(5.00) = %q, want %q", got, want)
	}
}
