package aji

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	internalaws "github.com/scttfrdmn/burst-core/internal/aws"
	internalcost "github.com/scttfrdmn/burst-core/internal/cost"
	internalsession "github.com/scttfrdmn/burst-core/internal/session"
	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// DetachedSession represents a burst session that can outlive the calling process.
// Use Submit[T,U] to start work, Collect[U] to retrieve results, and Cleanup to remove
// S3 artifacts.
type DetachedSession struct {
	sessionID  string
	bucket     string
	region     string
	fnName     string
	taskCount  int
	chunkCount int
	imageURI   string
	opts       options
}

// Attach reconnects to an existing detached session by session ID.
// The session must still have its manifest in S3.
func Attach(ctx context.Context, sessionID string) (*DetachedSession, error) {
	o := &options{}
	cfg, err := applyConfig(o)
	if err != nil {
		return nil, err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	s3c := internalaws.NewS3Client(awsCfg)
	m, err := internalsession.ReadManifest(ctx, s3c, cfg.S3Bucket, sessionID)
	if err != nil {
		return nil, fmt.Errorf("reading manifest for session %q: %w", sessionID, err)
	}
	return &DetachedSession{
		sessionID:  sessionID,
		bucket:     cfg.S3Bucket,
		region:     cfg.Region,
		taskCount:  m.TaskCount,
		chunkCount: m.ChunkCount,
	}, nil
}

// Status returns the current status of the detached session.
func (s *DetachedSession) Status(ctx context.Context) (*protocol.SessionStatus, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(s.region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	s3c := internalaws.NewS3Client(awsCfg)
	m, err := internalsession.ReadManifest(ctx, s3c, s.bucket, s.sessionID)
	if err != nil {
		return nil, err
	}
	complete, failed, err := countTaskStatuses(ctx, s3c, s.bucket, s.sessionID, s.taskCount)
	if err != nil {
		return nil, err
	}
	st := m.SessionStatus
	st.TasksComplete = complete
	st.TasksFailed = failed
	st.ElapsedSeconds = time.Since(m.CreatedAt).Seconds()
	return &st, nil
}

// Cleanup deletes all S3 artifacts for this session.
func (s *DetachedSession) Cleanup(ctx context.Context) error {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(s.region))
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}
	s3c := internalaws.NewS3Client(awsCfg)
	return internalsession.DeleteSession(ctx, s3c, s.bucket, s.sessionID)
}

// Submit[T, U any] is a package-level generic function that runs steps 1–5 of the
// worker lifecycle synchronously (environment snapshot → S3 upload → ECS launch) and
// returns the session ID. The calling process can exit after Submit returns; workers
// continue running in AWS.
//
// Go's type system does not allow generic methods, so this is a package-level function.
func Submit[T, U any](ctx context.Context, s *DetachedSession, items []T, fn func(T) (U, error)) (string, error) {
	if err := validateSerializable(items); err != nil {
		return "", err
	}
	fnName, err := registeredFnName(fn)
	if err != nil {
		return "", err
	}
	s.fnName = fnName

	cfg, err := applyConfig(&s.opts)
	if err != nil {
		return "", err
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(s.region))
	if err != nil {
		return "", fmt.Errorf("loading AWS config: %w", err)
	}
	s3c := internalaws.NewS3Client(awsCfg)
	ecsc := internalaws.NewECSClient(awsCfg)

	binaryPath, err := currentBinaryPath()
	if err != nil {
		return "", fmt.Errorf("determining binary path: %w", err)
	}
	envHash, err := EnvHash(binaryPath)
	if err != nil {
		return "", err
	}
	accountID := extractAccountID(cfg.ECRBaseURI)
	ecrc := internalaws.NewECRClient(awsCfg, accountID)
	s.imageURI = fmt.Sprintf("%s/burst-workers-go:%s", cfg.ECRBaseURI, envHash)

	// Chunk items
	chunks := chunkItems(items, s.opts.Workers)
	s.taskCount = len(chunks)
	s.chunkCount = len(chunks)

	// Generate session ID
	sessionID := protocol.GenerateSessionID(protocol.LangGo)
	s.sessionID = sessionID

	// Write initial manifest
	now := time.Now().UTC()
	m := &protocol.Manifest{
		SessionStatus: protocol.SessionStatus{
			SessionID:    sessionID,
			Language:     "go",
			Status:       protocol.StatusInitializing,
			TasksTotal:   len(chunks),
			CostEstimate: internalcost.EstimateCostPerHour(s.opts.CPU, s.opts.MemoryGB, s.opts.Workers),
			CreatedAt:    now,
			UpdatedAt:    now,
		},
		EnvHash:          envHash,
		LibraryVersion:   version,
		WorkersRequested: s.opts.Workers,
		WorkersActual:    s.opts.Workers,
		CPU:              s.opts.CPU,
		MemoryGB:         s.opts.MemoryGB,
		Backend:          s.opts.Backend,
		Spot:             s.opts.Spot,
		Region:           s.region,
		ChunkCount:       len(chunks),
		TaskCount:        len(chunks),
	}
	if err := internalsession.WriteManifest(ctx, s3c, s.bucket, m); err != nil {
		return "", fmt.Errorf("writing manifest: %w", err)
	}

	// Verify ECR image exists
	exists, err := ecrc.ImageExists(ctx, "burst-workers-go", envHash)
	if err != nil {
		return "", fmt.Errorf("checking ECR image: %w", err)
	}
	if !exists {
		return "", &BurstSetupError{
			Step:        "verify ECR image",
			Cause:       fmt.Sprintf("no image with tag %s in burst-workers-go", envHash),
			Remediation: "run aji.Setup() before submitting work",
		}
	}

	// Upload all task files
	if err := uploadTasks(ctx, uploadArgs{s3c: s3c, bucket: s.bucket, sessionID: sessionID, fnName: fnName}, chunks); err != nil {
		return "", err
	}

	// Get VPC network info
	vpcc := internalaws.NewVPCClient(awsCfg)
	subnets, err := vpcc.GetDefaultVPCSubnets(ctx)
	if err != nil {
		return "", err
	}
	sg, err := vpcc.GetDefaultSecurityGroup(ctx)
	if err != nil {
		return "", err
	}

	// Launch workers
	if err := launchWorkers(ctx, launchOpts{
		ecsc:             ecsc,
		s3c:              s3c,
		imageURI:         s.imageURI,
		sessionID:        sessionID,
		bucket:           s.bucket,
		region:           s.region,
		fnName:           fnName,
		taskCount:        len(chunks),
		cpu:              s.opts.CPU,
		memoryMB:         s.opts.MemoryGB * 1024,
		executionRoleARN: cfg.ExecutionRoleARN,
		taskRoleARN:      cfg.TaskRoleARN,
		subnets:          subnets,
		securityGroups:   []string{sg},
		spot:             s.opts.Spot,
		quotaVCPU:        cfg.FargateQuotaVCPU,
	}); err != nil {
		return "", err
	}

	// Update manifest to running
	m.Status = protocol.StatusRunning
	m.UpdatedAt = time.Now().UTC()
	_ = internalsession.WriteManifest(ctx, s3c, s.bucket, m)

	return sessionID, nil
}

// Collect[U any] is a package-level generic function that polls until all tasks complete
// and returns the assembled results in original item order.
//
// Go's type system does not allow generic methods, so this is a package-level function.
func Collect[U any](ctx context.Context, s *DetachedSession) ([]U, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(s.region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	s3c := internalaws.NewS3Client(awsCfg)

	if err := pollResults(ctx, s3c, s.bucket, s.sessionID, s.taskCount, os.Stdout); err != nil {
		return nil, err
	}
	payloads, err := downloadResults(ctx, s3c, s.bucket, s.sessionID, s.taskCount)
	if err != nil {
		return nil, err
	}
	return assembleResults[U](payloads)
}

// extractAccountID extracts the AWS account ID from an ECR base URI.
// ECR base URI format: {accountID}.dkr.ecr.{region}.amazonaws.com
func extractAccountID(ecrBaseURI string) string {
	parts := strings.SplitN(ecrBaseURI, ".", 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}
