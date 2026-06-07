// E2E tests for aji — run against real AWS.
//
// Usage:
//
//	AWS_PROFILE=aws BURST_E2E=1 go test -v -timeout 20m ./e2e/
//
// Run Setup first, then Map tests:
//
//	AWS_PROFILE=aws BURST_E2E=1 go test -v -timeout 20m -run "TestAjiSetup|TestAji" ./e2e/
package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/scttfrdmn/burst-core/aji"
)

func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("BURST_E2E") != "1" {
		t.Skip("set BURST_E2E=1 to run end-to-end tests against real AWS")
	}
}

func double(x int) (int, error)    { return x * 2, nil }
func square(x int) (int, error)    { return x * x, nil }
func failOnOdd(x int) (int, error) {
	if x%2 != 0 {
		return 0, fmt.Errorf("odd numbers not allowed: %d", x)
	}
	return x * 2, nil
}

func init() {
	aji.Register("double", double)
	aji.Register("square", square)
	aji.Register("failOnOdd", failOnOdd)

	if aji.IsWorkerMode() {
		os.Exit(aji.RunWorker(context.Background()))
	}
}

// workerBinary is the cross-compiled linux/amd64 test binary, built once per test run.
var (
	workerBinaryOnce sync.Once
	workerBinaryPath string
)

func getWorkerBinary(t *testing.T) string {
	t.Helper()
	workerBinaryOnce.Do(func() {
		_, thisFile, _, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("runtime.Caller failed")
		}
		moduleRoot := filepath.Join(filepath.Dir(thisFile), "..")

		// Build to a stable path so the hash is consistent within a test run
		dir, err := os.MkdirTemp("", "aji-e2e-worker-*")
		if err != nil {
			t.Fatalf("MkdirTemp: %v", err)
		}
		outPath := filepath.Join(dir, "e2e-worker.test")

		cmd := exec.Command("go", "test", "-c", "-o", outPath, "./e2e/")
		cmd.Dir = moduleRoot
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go test -c failed: %v\n%s", err, out)
		}
		workerBinaryPath = outPath
		t.Logf("built worker binary: %s", outPath)
	})
	return workerBinaryPath
}

// TestAjiSetup verifies Setup() cross-compiles and pushes a worker image to ECR.
// Must run before the Map tests.
func TestAjiSetup(t *testing.T) {
	requireE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	binaryPath := getWorkerBinary(t)
	t.Log("pushing worker image to ECR...")
	if err := aji.Setup(ctx, aji.WithBinaryPath(binaryPath)); err != nil {
		t.Fatalf("aji.Setup failed: %v", err)
	}
	t.Log("Setup complete")
}

// TestAjiSetupArm64 builds and pushes a Graviton (arm64) worker image.
func TestAjiSetupArm64(t *testing.T) {
	requireE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	moduleRoot := filepath.Join(filepath.Dir(thisFile), "..")
	outPath := filepath.Join(t.TempDir(), "e2e-worker-arm64.test")

	cmd := exec.Command("go", "test", "-c", "-o", outPath, "./e2e/")
	cmd.Dir = moduleRoot
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test -c (arm64) failed: %v\n%s", err, out)
	}

	t.Log("pushing arm64 worker image to ECR...")
	if err := aji.Setup(ctx,
		aji.WithBinaryPath(outPath),
		aji.WithSetupArch("arm64"),
	); err != nil {
		t.Fatalf("aji.Setup (arm64) failed: %v", err)
	}
	t.Log("arm64 Setup complete")
}

// mapWithWorkerBinary wraps aji.Map using the pre-built worker binary's env hash.
// This ensures Map looks for the same image that Setup pushed.
func mapWithWorkerBinary[T, U any](
	t *testing.T,
	ctx context.Context,
	items []T,
	fn func(T) (U, error),
	opts ...aji.Option,
) ([]U, error) {
	t.Helper()
	// aji.Map uses currentBinaryPath() for the hash, but we need the worker binary hash.
	// Pass the binary path explicitly via WithBinaryPath equivalent for Map.
	// Since aji.Map doesn't have WithBinaryPath, we set the env hash via a workaround:
	// override BURST_BINARY_PATH if the library supports it, otherwise use the
	// standard Map which uses os.Executable().
	//
	// For now, use standard Map — in tests the binary is the go test runner, and
	// we've set up Setup to match via WithBinaryPath.
	// TODO: aji.Map should accept WithBinaryPath to override the hash.
	return aji.Map(ctx, items, fn, opts...)
}

// TestAjiMapSmall verifies the full round-trip with a small item list.
func TestAjiMapSmall(t *testing.T) {
	requireE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	workerBin := getWorkerBinary(t)
	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}

	t.Logf("mapping %d items across 4 workers...", len(items))
	start := time.Now()

	results, err := aji.Map(ctx, items, double,
		aji.WithWorkers(4),
		aji.WithCPU(1),
		aji.WithMemory(2),
		aji.WithWorkerBinaryPath(workerBin),
	)
	if err != nil {
		t.Fatalf("Map failed: %v", err)
	}

	t.Logf("completed in %s", time.Since(start).Round(time.Second))
	if len(results) != len(items) {
		t.Fatalf("expected %d results, got %d", len(items), len(results))
	}
	for i, got := range results {
		if want := items[i] * 2; got != want {
			t.Errorf("results[%d] = %d, want %d", i, got, want)
		}
	}
	t.Logf("all %d results correct", len(results))
}

// TestAjiMapOrdering verifies results come back in original item order.
func TestAjiMapOrdering(t *testing.T) {
	requireE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	workerBin := getWorkerBinary(t)
	items := []int{5, 3, 8, 1, 9, 2, 7, 4, 6, 0}
	results, err := aji.Map(ctx, items, square,
		aji.WithWorkers(3),
		aji.WithWorkerBinaryPath(workerBin),
	)
	if err != nil {
		t.Fatalf("Map failed: %v", err)
	}
	for i, got := range results {
		if want := items[i] * items[i]; got != want {
			t.Errorf("results[%d] = %d, want %d (input %d)", i, got, want, items[i])
		}
	}
	t.Log("order preserved correctly")
}

// TestAjiMapPartialError verifies BurstPartialError when some items fail.
func TestAjiMapPartialError(t *testing.T) {
	requireE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	workerBin := getWorkerBinary(t)
	items := []int{0, 1, 2, 3, 4}
	_, err := aji.Map(ctx, items, failOnOdd,
		aji.WithWorkers(2),
		aji.WithWorkerBinaryPath(workerBin),
	)
	if err == nil {
		t.Fatal("expected BurstPartialError, got nil")
	}
	var partial *aji.BurstPartialError
	if !errors.As(err, &partial) {
		t.Fatalf("expected BurstPartialError, got %T: %v", err, err)
	}
	t.Logf("got expected partial error: %d failed, %d succeeded",
		partial.FailedCount, partial.SuccessCount)
}

// TestAjiLarger exercises chunking with more items than workers.
func TestAjiLarger(t *testing.T) {
	requireE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	const n = 100
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	workerBin := getWorkerBinary(t)
	t.Logf("mapping %d items across 5 workers...", n)
	results, err := aji.Map(ctx, items, double,
		aji.WithWorkers(5),
		aji.WithWorkerBinaryPath(workerBin),
	)
	if err != nil {
		t.Fatalf("Map failed: %v", err)
	}

	if len(results) != n {
		t.Fatalf("expected %d results, got %d", n, len(results))
	}
	for i, got := range results {
		if want := items[i] * 2; got != want {
			t.Errorf("results[%d] = %d, want %d", i, got, want)
		}
	}
	sorted := make([]int, len(results))
	copy(sorted, results)
	sort.Ints(sorted)
	for i, v := range sorted {
		if want := i * 2; v != want {
			t.Errorf("sorted[%d] = %d, want %d", i, v, want)
		}
	}
	t.Logf("all %d results correct", n)
}
