package aji

import (
	"context"
	"fmt"
	"os"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	internalaws "github.com/scttfrdmn/burst-core/internal/aws"
	internalcost "github.com/scttfrdmn/burst-core/internal/cost"
	internalsession "github.com/scttfrdmn/burst-core/internal/session"
	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// version is the aji library version, embedded in session manifests.
const version = "0.1.0"

// Result holds a single item's outcome from MapTolerant.
// Err is nil on success; Value is the zero value when Err is non-nil.
type Result[U any] struct {
	Value U
	Err   error
}

// Map distributes items across AWS ECS workers, calling fn on each item.
// fn must have been registered with Register() before calling Map().
// Results are returned in the same order as items.
//
// Errors:
//   - BurstPartialError if some tasks fail
//   - BurstCostLimitError if max_cost is hit before launching workers
//   - BurstQuotaError if workers were reduced due to quota (job still runs)
//   - BurstTimeoutError if context deadline is exceeded
func Map[T, U any](ctx context.Context, items []T, fn func(T) (U, error), opts ...Option) ([]U, error) {
	if err := validateSerializable(items); err != nil {
		return nil, err
	}
	fnName, err := registeredFnName(fn)
	if err != nil {
		return nil, err
	}

	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	cfg, err := applyConfig(o)
	if err != nil {
		return nil, err
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(o.Region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	s3c := internalaws.NewS3Client(awsCfg)
	ecsc := internalaws.NewECSClient(awsCfg)

	// Cost check before provisioning anything
	costPerHour := internalcost.EstimateCostPerHour(o.CPU, o.MemoryGB, o.Workers)
	if o.MaxCost > 0 && costPerHour > o.MaxCost {
		return nil, &BurstCostLimitError{
			Limit:         o.MaxCost,
			EstimatedCost: costPerHour,
		}
	}

	// Quota check
	quotac := internalaws.NewQuotaClient(awsCfg)
	quotaVCPU, _ := quotac.GetFargateOnDemandVCPUQuota(ctx)
	if o.Spot {
		if q, err := quotac.GetFargateSpotVCPUQuota(ctx); err == nil {
			quotaVCPU = q
		}
	}
	actualWorkers := o.Workers
	if quotaVCPU > 0 {
		maxWorkers := int(quotaVCPU) / o.CPU
		if maxWorkers < actualWorkers {
			actualWorkers = maxWorkers
			lines := internalcost.QuotaWarningLines(o.Workers, actualWorkers, float64(o.Workers*o.CPU), float64(actualWorkers*o.CPU))
			fmt.Fprintln(os.Stdout, lines[0])
			fmt.Fprintln(os.Stdout, lines[1])
		}
	}
	if o.CostAlert > 0 && costPerHour > o.CostAlert {
		fmt.Fprintln(os.Stdout, internalcost.CostAlertLine(o.CostAlert))
	}

	// Print start banner
	fmt.Fprintln(os.Stdout, internalcost.StartLine(actualWorkers))
	fmt.Fprintln(os.Stdout, internalcost.CostEstimateLine(costPerHour))
	fmt.Fprintln(os.Stdout, internalcost.ProcessingLine(len(items), actualWorkers))

	// Chunk items
	chunks := chunkItems(items, actualWorkers)
	avgItems := len(items) / len(chunks)
	fmt.Fprintln(os.Stdout, internalcost.ChunksLine(len(chunks), avgItems))

	// Generate session ID and write manifest
	sessionID := protocol.GenerateSessionID(protocol.LangGo)
	now := time.Now().UTC()

	// Resolve ECR image URI
	binaryPath := o.BinaryPath
	if binaryPath == "" {
		binaryPath, err = currentBinaryPath()
		if err != nil {
			return nil, fmt.Errorf("determining binary path: %w", err)
		}
	}
	envHash, err := EnvHash(binaryPath)
	if err != nil {
		return nil, err
	}
	arch := o.Arch
	if arch == "" {
		arch = "amd64"
	}
	accountID := extractAccountID(cfg.ECRBaseURI)
	ecrc := internalaws.NewECRClient(awsCfg, accountID)
	tag := envHash + "-" + arch
	imageURI := fmt.Sprintf("%s/burst-workers-go:%s", cfg.ECRBaseURI, tag)

	// Verify image exists
	exists, err := ecrc.ImageExists(ctx, "burst-workers-go", tag)
	if err != nil {
		return nil, fmt.Errorf("checking ECR image: %w", err)
	}
	if !exists {
		return nil, &BurstSetupError{
			Step:        "verify ECR image",
			Cause:       fmt.Sprintf("no image with tag %s in burst-workers-go", tag),
			Remediation: "run aji.Setup() before calling Map()",
		}
	}

	m := &protocol.Manifest{
		SessionStatus: protocol.SessionStatus{
			SessionID:    sessionID,
			Language:     "go",
			Status:       protocol.StatusInitializing,
			TasksTotal:   len(chunks),
			CostEstimate: costPerHour,
			CreatedAt:    now,
			UpdatedAt:    now,
		},
		EnvHash:          envHash,
		LibraryVersion:   version,
		WorkersRequested: o.Workers,
		WorkersActual:    actualWorkers,
		CPU:              o.CPU,
		MemoryGB:         o.MemoryGB,
		Backend:          o.Backend,
		Spot:             o.Spot,
		Region:           o.Region,
		ChunkCount:       len(chunks),
		TaskCount:        len(chunks),
	}
	if err := internalsession.WriteManifest(ctx, s3c, cfg.S3Bucket, m); err != nil {
		return nil, fmt.Errorf("writing manifest: %w", err)
	}

	// Upload all task files
	fmt.Fprintln(os.Stdout, "🚀 Submitting tasks...")
	if err := uploadTasks(ctx, uploadArgs{s3c: s3c, bucket: cfg.S3Bucket, sessionID: sessionID, fnName: fnName}, chunks); err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stdout, internalcost.SubmittedLine(len(chunks)))

	// Get VPC network info
	vpcc := internalaws.NewVPCClient(awsCfg)
	subnets, err := vpcc.GetDefaultVPCSubnets(ctx)
	if err != nil {
		return nil, err
	}
	sg, err := vpcc.GetDefaultSecurityGroup(ctx)
	if err != nil {
		return nil, err
	}

	// Launch workers (wave-based if quota limited)
	m.Status = protocol.StatusRunning
	m.UpdatedAt = time.Now().UTC()
	_ = internalsession.WriteManifest(ctx, s3c, cfg.S3Bucket, m)

	if err := launchWorkers(ctx, launchOpts{
		ecsc:             ecsc,
		s3c:              s3c,
		imageURI:         imageURI,
		sessionID:        sessionID,
		bucket:           cfg.S3Bucket,
		region:           o.Region,
		fnName:           fnName,
		taskCount:        len(chunks),
		cpu:              o.CPU,
		memoryMB:         o.MemoryGB * 1024,
		executionRoleARN: cfg.ExecutionRoleARN,
		taskRoleARN:      cfg.TaskRoleARN,
		subnets:          subnets,
		securityGroups:   []string{sg},
		spot:             o.Spot,
		quotaVCPU:        quotaVCPU,
		arch:             arch,
	}); err != nil {
		return nil, err
	}

	// Poll until complete
	startTime := time.Now()
	if err := pollResults(ctx, s3c, cfg.S3Bucket, sessionID, len(chunks), os.Stdout); err != nil {
		return nil, err
	}

	// Download and assemble results
	payloads, err := downloadResults(ctx, s3c, cfg.S3Bucket, sessionID, len(chunks))
	if err != nil {
		return nil, err
	}

	elapsed := time.Since(startTime)
	fmt.Fprintln(os.Stdout, internalcost.CompletedLine(elapsed))

	actualCost := internalcost.EstimateCost(o.CPU, o.MemoryGB, actualWorkers, elapsed.Hours())
	fmt.Fprintln(os.Stdout, internalcost.ActualCostLine(actualCost))

	// Update manifest
	m.Status = protocol.StatusComplete
	m.CostActual = actualCost
	m.UpdatedAt = time.Now().UTC()
	_ = internalsession.WriteManifest(ctx, s3c, cfg.S3Bucket, m)

	// Cleanup task/status files (keep manifest)
	go cleanupTaskFiles(context.Background(), s3c, cfg.S3Bucket, sessionID, len(chunks))
	go func() { _ = ecsc.DeregisterAllRevisions(context.Background(), "burst-"+sessionID) }()

	return assembleResults[U](payloads)
}

// MapTolerant distributes items across AWS ECS workers like Map, but never returns
// BurstPartialError. Per-item failures are reported via Result.Err.
// Infrastructure failures (AWS errors, timeouts) are still returned as error.
func MapTolerant[T, U any](ctx context.Context, items []T, fn func(T) (U, error), opts ...Option) ([]Result[U], error) {
	if err := validateSerializable(items); err != nil {
		return nil, err
	}
	fnName, err := registeredFnName(fn)
	if err != nil {
		return nil, err
	}

	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	cfg, err := applyConfig(o)
	if err != nil {
		return nil, err
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(o.Region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	s3c := internalaws.NewS3Client(awsCfg)
	ecsc := internalaws.NewECSClient(awsCfg)

	// Cost check before provisioning anything
	costPerHour := internalcost.EstimateCostPerHour(o.CPU, o.MemoryGB, o.Workers)
	if o.MaxCost > 0 && costPerHour > o.MaxCost {
		return nil, &BurstCostLimitError{
			Limit:         o.MaxCost,
			EstimatedCost: costPerHour,
		}
	}

	// Quota check
	quotac := internalaws.NewQuotaClient(awsCfg)
	quotaVCPU, _ := quotac.GetFargateOnDemandVCPUQuota(ctx)
	if o.Spot {
		if q, err := quotac.GetFargateSpotVCPUQuota(ctx); err == nil {
			quotaVCPU = q
		}
	}
	actualWorkers := o.Workers
	if quotaVCPU > 0 {
		maxWorkers := int(quotaVCPU) / o.CPU
		if maxWorkers < actualWorkers {
			actualWorkers = maxWorkers
			lines := internalcost.QuotaWarningLines(o.Workers, actualWorkers, float64(o.Workers*o.CPU), float64(actualWorkers*o.CPU))
			fmt.Fprintln(os.Stdout, lines[0])
			fmt.Fprintln(os.Stdout, lines[1])
		}
	}
	if o.CostAlert > 0 && costPerHour > o.CostAlert {
		fmt.Fprintln(os.Stdout, internalcost.CostAlertLine(o.CostAlert))
	}

	// Print start banner
	fmt.Fprintln(os.Stdout, internalcost.StartLine(actualWorkers))
	fmt.Fprintln(os.Stdout, internalcost.CostEstimateLine(costPerHour))
	fmt.Fprintln(os.Stdout, internalcost.ProcessingLine(len(items), actualWorkers))

	// Chunk items
	chunks := chunkItems(items, actualWorkers)
	avgItems := len(items) / len(chunks)
	fmt.Fprintln(os.Stdout, internalcost.ChunksLine(len(chunks), avgItems))

	// Generate session ID and write manifest
	sessionID := protocol.GenerateSessionID(protocol.LangGo)
	now := time.Now().UTC()

	// Resolve ECR image URI
	binaryPath := o.BinaryPath
	if binaryPath == "" {
		binaryPath, err = currentBinaryPath()
		if err != nil {
			return nil, fmt.Errorf("determining binary path: %w", err)
		}
	}
	envHash, err := EnvHash(binaryPath)
	if err != nil {
		return nil, err
	}
	arch := o.Arch
	if arch == "" {
		arch = "amd64"
	}
	accountID := extractAccountID(cfg.ECRBaseURI)
	ecrc := internalaws.NewECRClient(awsCfg, accountID)
	tag := envHash + "-" + arch
	imageURI := fmt.Sprintf("%s/burst-workers-go:%s", cfg.ECRBaseURI, tag)

	// Verify image exists
	exists, err := ecrc.ImageExists(ctx, "burst-workers-go", tag)
	if err != nil {
		return nil, fmt.Errorf("checking ECR image: %w", err)
	}
	if !exists {
		return nil, &BurstSetupError{
			Step:        "verify ECR image",
			Cause:       fmt.Sprintf("no image with tag %s in burst-workers-go", tag),
			Remediation: "run aji.Setup() before calling MapTolerant()",
		}
	}

	m := &protocol.Manifest{
		SessionStatus: protocol.SessionStatus{
			SessionID:    sessionID,
			Language:     "go",
			Status:       protocol.StatusInitializing,
			TasksTotal:   len(chunks),
			CostEstimate: costPerHour,
			CreatedAt:    now,
			UpdatedAt:    now,
		},
		EnvHash:          envHash,
		LibraryVersion:   version,
		WorkersRequested: o.Workers,
		WorkersActual:    actualWorkers,
		CPU:              o.CPU,
		MemoryGB:         o.MemoryGB,
		Backend:          o.Backend,
		Spot:             o.Spot,
		Region:           o.Region,
		ChunkCount:       len(chunks),
		TaskCount:        len(chunks),
	}
	if err := internalsession.WriteManifest(ctx, s3c, cfg.S3Bucket, m); err != nil {
		return nil, fmt.Errorf("writing manifest: %w", err)
	}

	// Upload all task files
	fmt.Fprintln(os.Stdout, "🚀 Submitting tasks...")
	if err := uploadTasks(ctx, uploadArgs{s3c: s3c, bucket: cfg.S3Bucket, sessionID: sessionID, fnName: fnName}, chunks); err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stdout, internalcost.SubmittedLine(len(chunks)))

	// Get VPC network info
	vpcc := internalaws.NewVPCClient(awsCfg)
	subnets, err := vpcc.GetDefaultVPCSubnets(ctx)
	if err != nil {
		return nil, err
	}
	sg, err := vpcc.GetDefaultSecurityGroup(ctx)
	if err != nil {
		return nil, err
	}

	// Launch workers (wave-based if quota limited)
	m.Status = protocol.StatusRunning
	m.UpdatedAt = time.Now().UTC()
	_ = internalsession.WriteManifest(ctx, s3c, cfg.S3Bucket, m)

	if err := launchWorkers(ctx, launchOpts{
		ecsc:             ecsc,
		s3c:              s3c,
		imageURI:         imageURI,
		sessionID:        sessionID,
		bucket:           cfg.S3Bucket,
		region:           o.Region,
		fnName:           fnName,
		taskCount:        len(chunks),
		cpu:              o.CPU,
		memoryMB:         o.MemoryGB * 1024,
		executionRoleARN: cfg.ExecutionRoleARN,
		taskRoleARN:      cfg.TaskRoleARN,
		subnets:          subnets,
		securityGroups:   []string{sg},
		spot:             o.Spot,
		quotaVCPU:        quotaVCPU,
		arch:             arch,
	}); err != nil {
		return nil, err
	}

	// Poll until complete
	startTime := time.Now()
	if err := pollResults(ctx, s3c, cfg.S3Bucket, sessionID, len(chunks), os.Stdout); err != nil {
		return nil, err
	}

	// Download and assemble results
	payloads, err := downloadResults(ctx, s3c, cfg.S3Bucket, sessionID, len(chunks))
	if err != nil {
		return nil, err
	}

	elapsed := time.Since(startTime)
	fmt.Fprintln(os.Stdout, internalcost.CompletedLine(elapsed))

	actualCost := internalcost.EstimateCost(o.CPU, o.MemoryGB, actualWorkers, elapsed.Hours())
	fmt.Fprintln(os.Stdout, internalcost.ActualCostLine(actualCost))

	// Update manifest
	m.Status = protocol.StatusComplete
	m.CostActual = actualCost
	m.UpdatedAt = time.Now().UTC()
	_ = internalsession.WriteManifest(ctx, s3c, cfg.S3Bucket, m)

	// Cleanup task/status files (keep manifest)
	go cleanupTaskFiles(context.Background(), s3c, cfg.S3Bucket, sessionID, len(chunks))
	go func() { _ = ecsc.DeregisterAllRevisions(context.Background(), "burst-"+sessionID) }()

	return assembleResultsTolerant[U](payloads)
}

// cleanupTaskFiles deletes .task, .result, .status, and .error files for a session.
func cleanupTaskFiles(ctx context.Context, s3c *internalaws.S3Client, bucket, sessionID string, taskCount int) {
	keys := protocol.S3Keys(bucket, sessionID)
	var toDelete []string
	for i := range taskCount {
		taskID := protocol.TaskID(i)
		toDelete = append(toDelete,
			keys.TaskKey(taskID),
			keys.ResultKey(taskID),
			keys.StatusKey(taskID),
			keys.ErrorKey(taskID),
		)
	}
	const batchSize = 1000
	for i := 0; i < len(toDelete); i += batchSize {
		end := i + batchSize
		if end > len(toDelete) {
			end = len(toDelete)
		}
		_ = s3c.DeleteObjects(ctx, bucket, toDelete[i:end])
	}
}

// Pool is a reusable cluster that amortizes worker startup across multiple Map calls.
type Pool struct {
	opts options
	cfg  interface{} // *internalconfig.Config, populated at creation
}

// NewPool creates a Pool with the given options. Validates config at creation time.
func NewPool(ctx context.Context, opts ...Option) (*Pool, error) {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	cfg, err := applyConfig(o)
	if err != nil {
		return nil, err
	}
	_ = cfg
	return &Pool{opts: *o, cfg: cfg}, nil
}

// Shutdown releases any resources held by the pool. Currently a no-op since workers
// are stateless, but should be called for forward compatibility.
func (p *Pool) Shutdown(_ context.Context) error { return nil }

// PoolMap[T, U any] runs Map using the pool's pre-configured options.
// Go does not support generic methods, so this is a package-level function.
func PoolMap[T, U any](ctx context.Context, p *Pool, items []T, fn func(T) (U, error)) ([]U, error) {
	opts := []Option{
		WithWorkers(p.opts.Workers),
		WithCPU(p.opts.CPU),
		WithMemory(p.opts.MemoryGB),
		WithBackend(p.opts.Backend),
		WithSpot(p.opts.Spot),
		WithRegion(p.opts.Region),
	}
	if p.opts.MaxCost > 0 {
		opts = append(opts, WithMaxCost(p.opts.MaxCost))
	}
	if p.opts.CostAlert > 0 {
		opts = append(opts, WithCostAlert(p.opts.CostAlert))
	}
	if p.opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.opts.Timeout)
		defer cancel()
	}
	return Map(ctx, items, fn, opts...)
}

// PoolMapTolerant[T, U any] runs MapTolerant using the pool's pre-configured options.
// Per-item failures are reported via Result.Err rather than returning BurstPartialError.
func PoolMapTolerant[T, U any](ctx context.Context, p *Pool, items []T, fn func(T) (U, error)) ([]Result[U], error) {
	opts := []Option{
		WithWorkers(p.opts.Workers),
		WithCPU(p.opts.CPU),
		WithMemory(p.opts.MemoryGB),
		WithBackend(p.opts.Backend),
		WithSpot(p.opts.Spot),
		WithRegion(p.opts.Region),
	}
	if p.opts.MaxCost > 0 {
		opts = append(opts, WithMaxCost(p.opts.MaxCost))
	}
	if p.opts.CostAlert > 0 {
		opts = append(opts, WithCostAlert(p.opts.CostAlert))
	}
	if p.opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.opts.Timeout)
		defer cancel()
	}
	return MapTolerant[T, U](ctx, items, fn, opts...)
}

// Session creates a DetachedSession with the given options. The session is not yet
// submitted — call Submit[T,U](ctx, s, items, fn) to begin work.
func Session(ctx context.Context, opts ...Option) (*DetachedSession, error) {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	cfg, err := applyConfig(o)
	if err != nil {
		return nil, err
	}
	return &DetachedSession{
		bucket: cfg.S3Bucket,
		region: o.Region,
		opts:   *o,
	}, nil
}

// SetupOption configures Setup behavior.
type SetupOption func(*setupOptions)

type setupOptions struct {
	BinaryPath string // explicit binary path; overrides cross-compilation
	Arch       string // "amd64" (default) or "arm64" (Graviton)
}

// WithBinaryPath provides an explicit pre-built linux binary instead of
// cross-compiling the current package.
func WithBinaryPath(p string) SetupOption {
	return func(o *setupOptions) { o.BinaryPath = p }
}

// WithSetupArch sets the CPU architecture for the worker image.
// "amd64" (default, x86_64) or "arm64" (Graviton2/3, ~20% cheaper).
func WithSetupArch(arch string) SetupOption {
	return func(o *setupOptions) { o.Arch = arch }
}

// Setup cross-compiles the current binary for linux/amd64, packages it into a
// minimal scratch Docker image, and pushes it to ECR. Idempotent — skips the build
// if ECR already has an image tagged with the current binary's env hash.
//
// Must be called once before using Map() in production. Safe to call multiple times.
func Setup(ctx context.Context, opts ...SetupOption) error {
	so := &setupOptions{}
	for _, opt := range opts {
		opt(so)
	}

	o := &options{}
	cfg, err := applyConfig(o)
	if err != nil {
		return err
	}

	binaryPath := so.BinaryPath
	if binaryPath == "" {
		binaryPath, err = currentBinaryPath()
		if err != nil {
			return fmt.Errorf("determining binary path: %w", err)
		}
	}

	envHash, err := EnvHash(binaryPath)
	if err != nil {
		return err
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}
	accountID := extractAccountID(cfg.ECRBaseURI)
	ecrc := internalaws.NewECRClient(awsCfg, accountID)

	arch := so.Arch
	if arch == "" {
		arch = "amd64"
	}
	imageURI, err := buildAndPushWorkerImage(ctx, ecrc, binaryPath, envHash, arch, os.Stderr)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "✓ aji setup complete (image: %s)\n", imageURI)
	return nil
}
