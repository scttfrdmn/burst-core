package aji

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	internalaws "github.com/scttfrdmn/burst-core/internal/aws"
	internalcost "github.com/scttfrdmn/burst-core/internal/cost"
	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// chunkItems splits items into n equal-sized chunks.
// The last chunk absorbs any remainder so all items are included.
func chunkItems[T any](items []T, n int) [][]T {
	if n <= 0 || len(items) == 0 {
		return nil
	}
	if n > len(items) {
		n = len(items)
	}
	size := len(items) / n
	chunks := make([][]T, n)
	start := 0
	for i := range chunks {
		end := start + size
		if i == n-1 {
			end = len(items) // last chunk absorbs remainder
		}
		chunks[i] = items[start:end]
		start = end
	}
	return chunks
}

type uploadArgs struct {
	s3c       *internalaws.S3Client
	bucket    string
	sessionID string
	fnName    string
}

// uploadTasks serializes and uploads all task files to S3 concurrently.
func uploadTasks[T any](ctx context.Context, args uploadArgs, chunks [][]T) error {
	const maxConcurrent = 20
	sem := make(chan struct{}, maxConcurrent)
	errCh := make(chan error, len(chunks))
	var wg sync.WaitGroup

	keys := protocol.S3Keys(args.bucket, args.sessionID)

	for i, chunk := range chunks {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, c []T) {
			defer wg.Done()
			defer func() { <-sem }()

			data, err := SerializeTask(args.fnName, idx, c)
			if err != nil {
				errCh <- fmt.Errorf("serializing chunk %d: %w", idx, err)
				return
			}
			taskID := protocol.TaskID(idx)
			if err := args.s3c.PutObject(ctx, args.bucket, keys.TaskKey(taskID), data); err != nil {
				errCh <- fmt.Errorf("uploading task %s: %w", taskID, err)
			}
		}(i, chunk)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		return err
	}
	return nil
}

type launchOpts struct {
	ecsc             *internalaws.ECSClient
	s3c              *internalaws.S3Client
	imageURI         string
	sessionID        string
	bucket           string
	region           string
	fnName           string
	taskCount        int
	cpu              int
	memoryMB         int
	executionRoleARN string
	taskRoleARN      string
	subnets          []string
	securityGroups   []string
	spot             bool
	quotaVCPU        float64
}

// launchWorkers registers one ECS task definition per session, then launches tasks in waves.
// Wave size is determined by the Fargate vCPU quota divided by per-task CPU.
// Each wave waits for completion before the next wave is launched.
func launchWorkers(ctx context.Context, opts launchOpts) error {
	family := "burst-" + opts.sessionID
	staticEnv := map[string]string{
		"BURST_SESSION_ID": opts.sessionID,
		"BURST_S3_BUCKET":  opts.bucket,
		"BURST_REGION":     opts.region,
		"BURST_LANG":       "go",
	}
	taskDefARN, err := opts.ecsc.RegisterTaskDefinition(
		ctx, family, opts.imageURI, opts.cpu, opts.memoryMB,
		opts.executionRoleARN, opts.taskRoleARN, staticEnv,
	)
	if err != nil {
		return fmt.Errorf("registering task definition: %w", err)
	}

	// Wave size: floor(quota vCPUs / per-task CPUs), capped at taskCount
	waveSize := opts.taskCount
	if opts.quotaVCPU > 0 && opts.cpu > 0 {
		ws := int(opts.quotaVCPU) / opts.cpu
		if ws > 0 && ws < waveSize {
			waveSize = ws
		}
	}

	for launched := 0; launched < opts.taskCount; launched += waveSize {
		end := launched + waveSize
		if end > opts.taskCount {
			end = opts.taskCount
		}
		for i := launched; i < end; i++ {
			taskID := protocol.TaskID(i)
			_, err := opts.ecsc.RunTask(ctx, internalaws.RunTaskOptions{
				TaskDefinitionARN: taskDefARN,
				Subnets:           opts.subnets,
				SecurityGroups:    opts.securityGroups,
				UseSpot:           opts.spot,
				ContainerEnvOverrides: map[string]string{
					"BURST_TASK_ID":       taskID,
					"BURST_FUNCTION_NAME": opts.fnName,
				},
			})
			if err != nil {
				return fmt.Errorf("launching task %s: %w", taskID, err)
			}
		}
		// Wait for this wave before launching the next one
		if end < opts.taskCount {
			if err := waitForTasks(ctx, opts.s3c, opts.bucket, opts.sessionID, launched, end); err != nil {
				return err
			}
		}
	}
	return nil
}

// waitForTasks polls S3 status files for tasks [start, end) until all are terminal.
func waitForTasks(ctx context.Context, s3c *internalaws.S3Client, bucket, sessionID string, start, end int) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			allDone := true
			for i := start; i < end; i++ {
				taskID := protocol.TaskID(i)
				keys := protocol.S3Keys(bucket, sessionID)
				data, err := s3c.GetObject(ctx, bucket, keys.StatusKey(taskID))
				if err != nil {
					allDone = false
					continue
				}
				switch string(data) {
				case "done", "failed", "partial":
					// terminal
				default:
					allDone = false
				}
			}
			if allDone {
				return nil
			}
		}
	}
}

// countTaskStatuses returns the count of done tasks and failed/partial tasks.
// Tasks whose status file does not yet exist are counted as pending (neither done nor failed).
func countTaskStatuses(ctx context.Context, s3c *internalaws.S3Client, bucket, sessionID string, taskCount int) (done, failed int, err error) {
	keys := protocol.S3Keys(bucket, sessionID)
	for i := range taskCount {
		taskID := protocol.TaskID(i)
		data, getErr := s3c.GetObject(ctx, bucket, keys.StatusKey(taskID))
		if getErr != nil {
			continue // pending
		}
		switch string(data) {
		case "done":
			done++
		case "failed", "partial":
			failed++
		}
	}
	return done, failed, nil
}

// pollResults polls S3 status files every 2 seconds until all tasks reach a terminal state.
// Progress lines are written to w. Returns BurstTimeoutError if ctx deadline is exceeded.
func pollResults(ctx context.Context, s3c *internalaws.S3Client, bucket, sessionID string, taskCount int, w io.Writer) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	start := time.Now()

	for {
		select {
		case <-ctx.Done():
			done, failed, _ := countTaskStatuses(ctx, s3c, bucket, sessionID, taskCount)
			return &BurstTimeoutError{
				SessionID: sessionID,
				Status: &protocol.SessionStatus{
					SessionID:      sessionID,
					Status:         protocol.StatusRunning,
					TasksTotal:     taskCount,
					TasksComplete:  done,
					TasksFailed:    failed,
					ElapsedSeconds: time.Since(start).Seconds(),
				},
			}
		case <-ticker.C:
			done, failed, err := countTaskStatuses(ctx, s3c, bucket, sessionID, taskCount)
			if err != nil {
				return err
			}
			fmt.Fprint(w, internalcost.ProgressLine(done+failed, taskCount, time.Since(start)))
			if done+failed == taskCount {
				return nil
			}
		}
	}
}

// downloadResults downloads all result files concurrently and returns them in task order.
func downloadResults(ctx context.Context, s3c *internalaws.S3Client, bucket, sessionID string, taskCount int) ([]*resultPayload, error) {
	keys := protocol.S3Keys(bucket, sessionID)
	payloads := make([]*resultPayload, taskCount)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errCh := make(chan error, taskCount)
	sem := make(chan struct{}, 20)

	for i := range taskCount {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			taskID := protocol.TaskID(idx)
			data, err := s3c.GetObject(ctx, bucket, keys.ResultKey(taskID))
			if err != nil {
				errCh <- fmt.Errorf("downloading result %s: %w", taskID, err)
				return
			}
			p, err := DeserializeResult(data)
			if err != nil {
				errCh <- fmt.Errorf("deserializing result %s: %w", taskID, err)
				return
			}
			mu.Lock()
			payloads[idx] = p
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		return nil, err
	}
	return payloads, nil
}

// assembleResults[U] flattens all result chunks into the original item order.
// Returns BurstPartialError if any items failed.
func assembleResults[U any](payloads []*resultPayload) ([]U, error) {
	var allRaw []json.RawMessage
	var allErrors []string
	anyFailed := false

	for _, p := range payloads {
		for i, raw := range p.Results {
			allRaw = append(allRaw, raw)
			errStr := ""
			if i < len(p.Errors) {
				errStr = p.Errors[i]
			}
			allErrors = append(allErrors, errStr)
			if errStr != "" {
				anyFailed = true
			}
		}
	}

	if anyFailed {
		failedCount, successCount := 0, 0
		for _, e := range allErrors {
			if e != "" {
				failedCount++
			} else {
				successCount++
			}
		}
		return nil, &BurstPartialError{
			Results:      allRaw,
			Errors:       allErrors,
			FailedCount:  failedCount,
			SuccessCount: successCount,
		}
	}

	results := make([]U, len(allRaw))
	for i, raw := range allRaw {
		if err := json.Unmarshal(raw, &results[i]); err != nil {
			return nil, fmt.Errorf("deserializing result[%d]: %w", i, err)
		}
	}
	return results, nil
}
