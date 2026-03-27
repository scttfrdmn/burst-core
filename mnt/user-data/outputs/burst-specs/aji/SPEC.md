# aji — Go Cloud Bursting Library

## Overview

`aji` is a Go library (and sub-package of `burst-core`) that provides cloud bursting
to AWS for Go workloads. Unlike every other library in the burst family, `aji` does
not serialize functions or capture a package environment. Instead, the user's own
binary is cross-compiled for Linux and becomes the worker. Workers are dispatched
by registered function name.

The name is a Go board game term: aji means latent potential — stones that appear
dormant but can be activated. Cloud capacity that waits silently and fires on demand.

Read ARCHITECTURE.md before implementing. All session management, S3 key schema,
cost reporting format, error types, and worker lifecycle must conform to that spec.

---

## Repository Structure

`aji` lives as a sub-package within `burst-core`:

```
burst-core/
├── aji/
│   ├── aji.go               # Public API: Map, Pool, session functions
│   ├── registry.go          # Function registry
│   ├── worker.go            # Worker mode entrypoint
│   ├── executor.go          # ECS launch and polling logic
│   ├── serialize.go         # encoding/json + encoding/gob serialization
│   ├── compile.go           # Cross-compilation of user binary
│   ├── session.go           # Session lifecycle
│   ├── errors.go            # Error types
│   ├── cost.go              # Cost estimation and display
│   └── config.go            # Config loading (delegates to burst-core/internal/config)
├── ... (rest of burst-core)
```

Import path: `github.com/scttfrdmn/burst-core/aji`

Users add this to their `go.mod`:
```
require github.com/scttfrdmn/burst-core v0.1.0
```

---

## Language and Tooling

- Go 1.22+ (required for generics improvements and range-over-function)
- Uses `github.com/aws/aws-sdk-go-v2` (already in burst-core)
- No additional dependencies beyond burst-core's existing go.mod

---

## The Worker-as-Binary Pattern — Critical Design Decision

Go functions are not serializable. The solution is architectural:

**The user's binary IS the worker.**

1. User registers functions with `aji.Register()` in their `init()` or `main()`
2. User calls `aji.Setup()` once — this cross-compiles their binary for `linux/amd64`
   and packages it into a minimal Docker image
3. On `Map()`, ECS tasks run the user's own binary with `--aji-worker` flag
4. Workers read the function name from env var, look it up in the registry, execute it

This means:
- **No Docker build during development** — the binary is the environment
- **Zero cold start problem** — Go binaries start in milliseconds
- **Type safety** — the function registry uses Go generics
- **No serialization of functions** — impossible and unnecessary
- **10MB container image** — `scratch` base image with just the binary

### Cross-compilation

```go
func buildWorkerBinary(ctx context.Context) (string, error) {
    // Determine the main package of the calling binary
    // Cross-compile for linux/amd64:
    cmd := exec.CommandContext(ctx, "go", "build",
        "-o", workerBinaryPath,
        ".",
    )
    cmd.Env = append(os.Environ(),
        "GOOS=linux",
        "GOARCH=amd64",
        "CGO_ENABLED=0",
    )
    // ...
}
```

The binary is uploaded to S3 at `artifacts/go/{version}/worker-linux-amd64`.
Workers download it on first run and cache it at the env-hash path.

**Alternative for users who want to be explicit:**
```go
aji.Setup(ctx, aji.SetupOptions{
    BinaryPath: "./myapp",  // explicit binary, already compiled
})
```

### Worker Dockerfile

Generated during `aji.Setup()`:

```dockerfile
FROM scratch
COPY worker-linux-amd64 /worker
ENTRYPOINT ["/worker", "--aji-worker"]
```

Result is a ~10MB Docker image. No OS, no shell, no package manager.

---

## Function Registry (`registry.go`)

```go
package aji

import "sync"

var registry sync.Map  // map[string]registeredFn

type registeredFn struct {
    inputType  reflect.Type
    outputType reflect.Type
    fn         any
}

// Register registers a function for use as a burst worker.
// Must be called before Map() or Setup().
// Typically called from init() or main().
//
// fn must be of type func(T) (U, error) or func(T) U
// where T and U are JSON-serializable types.
func Register[T, U any](name string, fn func(T) (U, error)) {
    registry.Store(name, registeredFn{
        inputType:  reflect.TypeOf((*T)(nil)).Elem(),
        outputType: reflect.TypeOf((*U)(nil)).Elem(),
        fn:         fn,
    })
}

// RegisterSimple registers a function that doesn't return an error.
func RegisterSimple[T, U any](name string, fn func(T) U) {
    Register(name, func(item T) (U, error) {
        return fn(item), nil
    })
}
```

---

## Public API (`aji.go`)

```go
package aji

// Map distributes items across AWS ECS workers, calling fn on each item.
// fn must have been registered with Register() before calling Map().
// Results are returned in the same order as items.
//
// This is the primary API for aji.
func Map[T, U any](
    ctx context.Context,
    items []T,
    fn func(T) (U, error),
    opts ...Option,
) ([]U, error)

// Pool is a reusable cluster that amortizes worker startup across multiple Map calls.
type Pool struct { ... }

func NewPool(ctx context.Context, opts ...Option) (*Pool, error)

func (p *Pool) Map[T, U any](
    ctx context.Context,
    items []T,
    fn func(T) (U, error),
) ([]U, error)

func (p *Pool) Shutdown(ctx context.Context) error

// Session starts a detached session.
func Session(ctx context.Context, opts ...Option) (*DetachedSession, error)

type DetachedSession struct { ... }

func (s *DetachedSession) Submit[T, U any](
    ctx context.Context,
    items []T,
    fn func(T) (U, error),
) (string, error)  // returns session ID

// Attach reconnects to an existing detached session.
func Attach(ctx context.Context, sessionID string) (*DetachedSession, error)

func (s *DetachedSession) Status(ctx context.Context) (*protocol.SessionStatus, error)
func (s *DetachedSession) Collect[U any](ctx context.Context) ([]U, error)
func (s *DetachedSession) Cleanup(ctx context.Context) error

// Setup cross-compiles the current binary and provisions the ECR image.
// Must be called once before using Map() in production.
// Safe to call multiple times — skips if env hash unchanged.
func Setup(ctx context.Context, opts ...SetupOption) error
```

### Options

```go
type Option func(*options)

func WithWorkers(n int) Option
func WithCPU(cpu int) Option
func WithMemory(memoryGB int) Option
func WithBackend(backend string) Option  // "fargate" or "ec2"
func WithSpot(spot bool) Option
func WithMaxCost(usd float64) Option
func WithCostAlert(usd float64) Option
func WithRegion(region string) Option
func WithTimeout(d time.Duration) Option
```

---

## Worker Mode (`worker.go`)

When the binary is invoked with `--aji-worker`, it runs in worker mode:

```go
func runWorker(ctx context.Context) error {
    sessionID := os.Getenv("BURST_SESSION_ID")
    taskID    := os.Getenv("BURST_TASK_ID")
    bucket    := os.Getenv("BURST_S3_BUCKET")
    region    := os.Getenv("BURST_REGION")
    fnName    := os.Getenv("BURST_FUNCTION_NAME")

    s3client := s3.NewFromConfig(awsConfig(region))
    keys := protocol.S3Keys(bucket, sessionID)

    // Signal running
    putStatus(ctx, s3client, keys.StatusKey(taskID), "running")

    // Look up registered function
    entry, ok := registry.Load(fnName)
    if !ok {
        putStatus(ctx, s3client, keys.StatusKey(taskID), "failed")
        putError(ctx, s3client, keys.ErrorKey(taskID),
            fmt.Sprintf("function %q not registered", fnName))
        return fmt.Errorf("function %q not registered", fnName)
    }

    // Download and deserialize task
    taskData, err := getS3(ctx, s3client, bucket, keys.TaskKey(taskID))
    if err != nil { return handleWorkerError(...) }

    // Deserialize items (JSON array)
    // Reflect to get the correct type for the registered function
    items, err := deserializeItems(taskData, entry.inputType)
    if err != nil { return handleWorkerError(...) }

    // Execute function on each item
    results := make([]any, len(items))
    errs    := make([]string, len(items))
    anyFailed := false
    for i, item := range items {
        result, err := callRegistered(entry, item)
        if err != nil {
            errs[i] = err.Error()
            anyFailed = true
        } else {
            results[i] = result
        }
    }

    // Upload result
    resultData, _ := json.Marshal(map[string]any{
        "results": results,
        "errors":  errs,
    })
    putS3(ctx, s3client, bucket, keys.ResultKey(taskID), resultData)

    if anyFailed {
        putStatus(ctx, s3client, keys.StatusKey(taskID), "partial")
    } else {
        putStatus(ctx, s3client, keys.StatusKey(taskID), "done")
    }
    return nil
}
```

### Worker mode detection in main()

Users must add this to their `main()`:

```go
func main() {
    // aji worker mode — must be first in main()
    if aji.IsWorkerMode() {
        os.Exit(aji.RunWorker(context.Background()))
    }

    // ... normal application code
}
```

Document this clearly. Provide a code snippet users copy into their main.go.

`IsWorkerMode()` checks for `--aji-worker` in os.Args OR `BURST_WORKER=1` env var.

---

## Serialization (`serialize.go`)

For task data (inputs/outputs), use `encoding/json` by default.

```go
// Task file format: JSON array of items
// {"items": [...], "function": "fnName", "chunk_index": 0}

type taskPayload struct {
    Items       json.RawMessage `json:"items"`
    FunctionName string         `json:"function"`
    ChunkIndex  int            `json:"chunk_index"`
}

type resultPayload struct {
    Results []json.RawMessage `json:"results"`
    Errors  []string          `json:"errors"` // empty string = no error
}
```

For high-throughput numerical data (slices of float64, int, etc.), offer
`encoding/gob` via `WithGobSerialization()` option. Gob is ~3x faster and ~2x smaller
than JSON for float arrays.

Users must ensure their types are JSON-marshalable (exported fields, no channels,
no functions). If marshaling fails, return a clear error at Map() call time, before
any AWS resources are provisioned.

```go
func validateSerializable[T any](items []T) error {
    if len(items) == 0 { return nil }
    // Try marshaling the first item as a smoke test
    _, err := json.Marshal(items[0])
    if err != nil {
        return fmt.Errorf("aji: items must be JSON-serializable; "+
            "first item marshal failed: %w", err)
    }
    return nil
}
```

---

## Error Types (`errors.go`)

```go
package aji

// BurstPartialError is returned when some tasks complete and some fail.
// Callers should always check for this type when processing results.
type BurstPartialError struct {
    Results      []json.RawMessage // nil entries where task failed
    Errors       []string          // empty string entries where task succeeded
    FailedCount  int
    SuccessCount int
}
func (e *BurstPartialError) Error() string

// BurstQuotaError is returned when AWS quota prevents launching requested workers.
type BurstQuotaError struct {
    RequestedWorkers int
    ActualWorkers    int
    QuotaName        string
    QuotaValue       float64
}
func (e *BurstQuotaError) Error() string

// BurstCostLimitError is returned when the job hits the configured cost ceiling.
type BurstCostLimitError struct {
    Limit          float64
    EstimatedCost  float64
    PartialResults []json.RawMessage
}
func (e *BurstCostLimitError) Error() string

// BurstTimeoutError is returned when the context deadline is exceeded.
type BurstTimeoutError struct {
    SessionID      string
    Status         *protocol.SessionStatus
}
func (e *BurstTimeoutError) Error() string

// BurstSetupError is returned when AWS resource provisioning fails.
type BurstSetupError struct {
    Step        string
    Cause       string
    Remediation string
}
func (e *BurstSetupError) Error() string
```

### Error handling idiom

Callers should use errors.As() for structured error handling:

```go
results, err := aji.Map(ctx, items, processItem, aji.WithWorkers(50))
if err != nil {
    var partialErr *aji.BurstPartialError
    var quotaErr   *aji.BurstQuotaError
    var costErr    *aji.BurstCostLimitError

    switch {
    case errors.As(err, &partialErr):
        log.Printf("%d/%d tasks failed",
            partialErr.FailedCount,
            partialErr.FailedCount + partialErr.SuccessCount)
        // process partialErr.Results for successful items
    case errors.As(err, &quotaErr):
        log.Printf("quota limited to %d workers (requested %d)",
            quotaErr.ActualWorkers, quotaErr.RequestedWorkers)
    case errors.As(err, &costErr):
        log.Printf("cost limit $%.2f hit (estimated $%.2f)",
            costErr.Limit, costErr.EstimatedCost)
    default:
        log.Fatal(err)
    }
}
```

Document this pattern prominently. It is Go's idiomatic error handling applied
correctly to distributed computing.

---

## Complete Usage Example

This is what a user's Go file looks like. Include this verbatim in README.md:

```go
package main

import (
    "context"
    "fmt"
    "log"
    "math"
    "os"

    "github.com/scttfrdmn/burst-core/aji"
)

// Register functions in init() — before main() runs
func init() {
    aji.Register("computePrime", computePrime)
}

func computePrime(n int) (bool, error) {
    if n < 2 { return false, nil }
    for i := 2; i <= int(math.Sqrt(float64(n))); i++ {
        if n%i == 0 { return false, nil }
    }
    return true, nil
}

func main() {
    // aji worker mode check — must be first
    if aji.IsWorkerMode() {
        os.Exit(aji.RunWorker(context.Background()))
    }

    ctx := context.Background()

    // One-time setup (skipped if already done)
    if err := aji.Setup(ctx); err != nil {
        log.Fatal(err)
    }

    // Generate 1 million candidate numbers
    candidates := make([]int, 1_000_000)
    for i := range candidates {
        candidates[i] = i + 2
    }

    // Find all primes using 100 cloud workers
    results, err := aji.Map(ctx, candidates, computePrime,
        aji.WithWorkers(100),
        aji.WithCPU(2),
    )
    if err != nil {
        log.Fatal(err)
    }

    primes := 0
    for _, isPrime := range results {
        if isPrime { primes++ }
    }
    fmt.Printf("Found %d primes in first million integers\n", primes)
}
```

---

## `Setup()` Behavior

```go
func Setup(ctx context.Context, opts ...SetupOption) error {
    // 1. Check that burst-core config exists (~/.burst/config.json)
    //    If not, print message and call burst-core setup
    // 2. Compute env hash: SHA256 of the current binary's modification time + size
    //    (or explicit binary path if provided via SetupOptions)
    // 3. Check ECR for image tagged with env hash — skip if present
    // 4. Cross-compile current package for linux/amd64
    // 5. Build Docker image from scratch + binary
    // 6. Push to ECR as burst-workers-go:{env-hash}
    // 7. Store env hash in config for subsequent Map() calls
}
```

---

## Testing

- Unit tests for registry, serialization, worker mode detection
- Table-driven tests for error type assertions
- Integration tests require `BURST_INTEGRATION_TEST=1` and real AWS credentials
- Integration test: register a simple function, run Map() with 5 workers, verify results
- Integration test: verify cross-compiled binary runs correctly inside scratch container
- Benchmark: measure Map() overhead vs local goroutines for varying item counts
- Use `testcontainers-go` to run a scratch container locally for worker tests

## Documentation

- Full GoDoc comments on all exported symbols
- README with quick start (copy-paste the example above)
- Explicit documentation of the `init()` + `IsWorkerMode()` pattern — this is the
  most unfamiliar part for new users and must be explained clearly
- FAQ: "Why can't I use a lambda/closure?" — explain why function registration is
  required and why it's actually safer than cloudpickle
