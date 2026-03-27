package aji

import (
	"context"
	"fmt"
	"os"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	internalaws "github.com/scttfrdmn/burst-core/internal/aws"
	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// IsWorkerMode returns true if the binary was invoked with --aji-worker flag
// or the BURST_WORKER=1 environment variable is set.
//
// Users must add this check as the first line of main():
//
//	func main() {
//	    if aji.IsWorkerMode() {
//	        os.Exit(aji.RunWorker(context.Background()))
//	    }
//	    // ... normal application code
//	}
func IsWorkerMode() bool {
	if os.Getenv("BURST_WORKER") == "1" {
		return true
	}
	for _, arg := range os.Args[1:] {
		if arg == "--aji-worker" {
			return true
		}
	}
	return false
}

// RunWorker executes in worker mode: downloads a task from S3, executes the registered
// function on each item, and uploads the result. Returns an exit code for os.Exit.
func RunWorker(ctx context.Context) int {
	if err := runWorker(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "aji worker error: %v\n", err)
		return 1
	}
	return 0
}

func runWorker(ctx context.Context) error {
	sessionID := os.Getenv("BURST_SESSION_ID")
	taskID    := os.Getenv("BURST_TASK_ID")
	bucket    := os.Getenv("BURST_S3_BUCKET")
	region    := os.Getenv("BURST_REGION")
	fnName    := os.Getenv("BURST_FUNCTION_NAME")

	for _, pair := range []struct{ key, val string }{
		{"BURST_SESSION_ID", sessionID},
		{"BURST_TASK_ID", taskID},
		{"BURST_S3_BUCKET", bucket},
		{"BURST_REGION", region},
		{"BURST_FUNCTION_NAME", fnName},
	} {
		if pair.val == "" {
			return fmt.Errorf("missing required env var: %s", pair.key)
		}
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}
	s3c := internalaws.NewS3Client(awsCfg)
	keys := protocol.S3Keys(bucket, sessionID)

	// Signal running
	if err := s3c.PutObject(ctx, bucket, keys.StatusKey(taskID), []byte("running")); err != nil {
		return fmt.Errorf("writing running status: %w", err)
	}

	entry, ok := lookupEntry(fnName)
	if !ok {
		errMsg := fmt.Sprintf("function %q not registered", fnName)
		_ = s3c.PutObject(ctx, bucket, keys.ErrorKey(taskID), []byte(errMsg))
		_ = s3c.PutObject(ctx, bucket, keys.StatusKey(taskID), []byte("failed"))
		return fmt.Errorf("%s", errMsg)
	}

	// Download task
	taskData, err := s3c.GetObject(ctx, bucket, keys.TaskKey(taskID))
	if err != nil {
		msg := fmt.Sprintf("downloading task: %v", err)
		_ = s3c.PutObject(ctx, bucket, keys.ErrorKey(taskID), []byte(msg))
		_ = s3c.PutObject(ctx, bucket, keys.StatusKey(taskID), []byte("failed"))
		return fmt.Errorf("downloading task: %w", err)
	}

	payload, err := DeserializeTask(taskData)
	if err != nil {
		msg := fmt.Sprintf("deserializing task: %v", err)
		_ = s3c.PutObject(ctx, bucket, keys.ErrorKey(taskID), []byte(msg))
		_ = s3c.PutObject(ctx, bucket, keys.StatusKey(taskID), []byte("failed"))
		return fmt.Errorf("deserializing task: %w", err)
	}

	items, err := DeserializeItems(payload.Items, entry.inputType)
	if err != nil {
		msg := fmt.Sprintf("deserializing items: %v", err)
		_ = s3c.PutObject(ctx, bucket, keys.ErrorKey(taskID), []byte(msg))
		_ = s3c.PutObject(ctx, bucket, keys.StatusKey(taskID), []byte("failed"))
		return fmt.Errorf("deserializing items: %w", err)
	}

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
	resultData, err := SerializeResult(results, errs)
	if err != nil {
		msg := fmt.Sprintf("serializing result: %v", err)
		_ = s3c.PutObject(ctx, bucket, keys.ErrorKey(taskID), []byte(msg))
		_ = s3c.PutObject(ctx, bucket, keys.StatusKey(taskID), []byte("failed"))
		return fmt.Errorf("serializing result: %w", err)
	}

	if err := s3c.PutObject(ctx, bucket, keys.ResultKey(taskID), resultData); err != nil {
		return fmt.Errorf("uploading result: %w", err)
	}

	status := "done"
	if anyFailed {
		status = "partial"
	}
	return s3c.PutObject(ctx, bucket, keys.StatusKey(taskID), []byte(status))
}
