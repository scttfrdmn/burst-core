// Package aji provides cloud bursting for Go workloads via AWS ECS.
//
// Unlike other burst family libraries, aji does not serialize functions. Instead,
// the user's own binary IS the worker: register functions with Register(), call
// Setup() once to cross-compile and push to ECR, then call Map() to distribute work.
//
// Quick start:
//
//	func init() {
//	    aji.Register("process", process)
//	}
//
//	func main() {
//	    if aji.IsWorkerMode() {
//	        os.Exit(aji.RunWorker(context.Background()))
//	    }
//	    // ... normal application code
//	}
package aji

import (
	"errors"
	"fmt"
	"time"

	internalconfig "github.com/scttfrdmn/burst-core/internal/config"
)

type options struct {
	Workers    int
	CPU        int
	MemoryGB   int
	Backend    string
	Spot       bool
	MaxCost    float64
	CostAlert  float64
	Region     string
	Timeout    time.Duration
	Arch       string // "amd64" (default) or "arm64" (Graviton — ~20% cheaper)
	BinaryPath string // override binary path for env hash (e.g. pre-built linux binary)
}

// Option is a functional option for Map, Pool, and Session.
type Option func(*options)

// WithWorkers sets the number of ECS workers to launch.
func WithWorkers(n int) Option { return func(o *options) { o.Workers = n } }

// WithCPU sets the number of vCPUs per worker (must be a valid Fargate vCPU value).
func WithCPU(cpu int) Option { return func(o *options) { o.CPU = cpu } }

// WithMemory sets the memory in GB per worker.
func WithMemory(gb int) Option { return func(o *options) { o.MemoryGB = gb } }

// WithBackend sets the compute backend: "fargate" (default) or "ec2".
func WithBackend(b string) Option { return func(o *options) { o.Backend = b } }

// WithSpot enables Fargate Spot for lower cost (with possible interruption).
func WithSpot(spot bool) Option { return func(o *options) { o.Spot = spot } }

// WithMaxCost sets a maximum cost ceiling in USD. Map returns BurstCostLimitError if exceeded.
func WithMaxCost(usd float64) Option { return func(o *options) { o.MaxCost = usd } }

// WithCostAlert sets a cost alert threshold in USD. A warning is printed if exceeded.
func WithCostAlert(usd float64) Option { return func(o *options) { o.CostAlert = usd } }

// WithRegion overrides the AWS region from ~/.burst/config.json.
func WithRegion(r string) Option { return func(o *options) { o.Region = r } }

// WithTimeout sets a maximum duration to wait for results.
func WithTimeout(d time.Duration) Option { return func(o *options) { o.Timeout = d } }

// WithArch sets the CPU architecture for ECS workers: "amd64" (default, x86_64) or
// "arm64" (Graviton2/3, ~20% cheaper and often faster for compute workloads).
// The worker image is built for the specified architecture.
func WithArch(arch string) Option { return func(o *options) { o.Arch = arch } }

// WithWorkerBinaryPath overrides the binary used to compute the env hash for ECR
// image lookup. Useful when the currently running binary (e.g. a test runner) differs
// from the linux binary that was pushed via Setup(WithBinaryPath(...)).
func WithWorkerBinaryPath(p string) Option { return func(o *options) { o.BinaryPath = p } }

// applyConfig merges ~/.burst/config.json defaults under any explicitly set options.
// Returns the loaded Config for callers that need resource ARNs, bucket names, etc.
func applyConfig(o *options) (*internalconfig.Config, error) {
	cfg, err := internalconfig.Load()
	if err != nil {
		if errors.Is(err, internalconfig.ErrNotFound) {
			return nil, &BurstSetupError{
				Step:        "load config",
				Cause:       "~/.burst/config.json not found",
				Remediation: "run: burst-core setup",
			}
		}
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if o.Workers == 0 {
		o.Workers = cfg.DefaultWorkers
	}
	if o.CPU == 0 {
		o.CPU = cfg.DefaultCPU
	}
	if o.MemoryGB == 0 {
		o.MemoryGB = cfg.DefaultMemoryGB
	}
	if o.Backend == "" {
		o.Backend = cfg.Backend
	}
	if !o.Spot {
		o.Spot = cfg.Spot
	}
	if o.MaxCost == 0 {
		o.MaxCost = cfg.MaxCostPerJob
	}
	if o.CostAlert == 0 {
		o.CostAlert = cfg.CostAlertThreshold
	}
	if o.Region == "" {
		o.Region = cfg.Region
	}
	return cfg, nil
}
