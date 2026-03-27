// Package aws provides thin, testable wrappers around AWS SDK v2 clients.
// Each file in this package covers one AWS service.
package aws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// s3API is a narrow interface over the subset of s3.Client methods used here.
// The real client (s3.Client) satisfies this interface.
type s3API interface {
	HeadBucket(ctx context.Context, in *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	CreateBucket(ctx context.Context, in *s3.CreateBucketInput, opts ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
	PutBucketEncryption(ctx context.Context, in *s3.PutBucketEncryptionInput, opts ...func(*s3.Options)) (*s3.PutBucketEncryptionOutput, error)
	PutPublicAccessBlock(ctx context.Context, in *s3.PutPublicAccessBlockInput, opts ...func(*s3.Options)) (*s3.PutPublicAccessBlockOutput, error)
	PutBucketLifecycleConfiguration(ctx context.Context, in *s3.PutBucketLifecycleConfigurationInput, opts ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, opts ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteBucket(ctx context.Context, in *s3.DeleteBucketInput, opts ...func(*s3.Options)) (*s3.DeleteBucketOutput, error)
}

// S3Client wraps S3 operations used by burst-core.
type S3Client struct {
	client s3API
	region string
}

// NewS3Client creates an S3Client from an AWS config.
func NewS3Client(cfg aws.Config) *S3Client {
	return &S3Client{client: s3.NewFromConfig(cfg), region: cfg.Region}
}

// BucketExists returns true if the bucket exists and is accessible.
func (c *S3Client) BucketExists(ctx context.Context, bucket string) (bool, error) {
	_, err := c.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err == nil {
		return true, nil
	}
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return false, nil
	}
	// 403 means bucket exists but is owned by another account
	if isS3Forbidden(err) {
		return false, &protocol.BurstSetupError{
			Step:        "check S3 bucket",
			Cause:       fmt.Sprintf("bucket %q exists but is owned by another account", bucket),
			Remediation: "choose a different bucket name in your config",
		}
	}
	return false, err
}

// CreateBucket creates an S3 bucket idempotently and applies the standard
// burst-core bucket configuration (encryption, public access block, lifecycle rules).
func (c *S3Client) CreateBucket(ctx context.Context, bucket string) error {
	exists, err := c.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	in := &s3.CreateBucketInput{Bucket: aws.String(bucket)}
	// us-east-1 is the default region and does not accept a LocationConstraint
	if c.region != "us-east-1" {
		in.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(c.region),
		}
	}

	if _, err := c.client.CreateBucket(ctx, in); err != nil {
		// Race condition: another process created the bucket between our check and create
		if isS3AlreadyOwnedByYou(err) {
			return nil
		}
		return &protocol.BurstSetupError{
			Step:  "create S3 bucket",
			Cause: err.Error(),
		}
	}

	if err := c.configureBucket(ctx, bucket); err != nil {
		return err
	}
	return nil
}

// configureBucket applies encryption, public access block, and lifecycle rules.
func (c *S3Client) configureBucket(ctx context.Context, bucket string) error {
	// SSE-S3 encryption
	_, err := c.client.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: aws.String(bucket),
		ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
			Rules: []types.ServerSideEncryptionRule{{
				ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{
					SSEAlgorithm: types.ServerSideEncryptionAes256,
				},
				BucketKeyEnabled: aws.Bool(true),
			}},
		},
	})
	if err != nil {
		return &protocol.BurstSetupError{Step: "configure S3 bucket encryption", Cause: err.Error()}
	}

	// Block all public access
	t := true
	_, err = c.client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(bucket),
		PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(t),
			BlockPublicPolicy:     aws.Bool(t),
			IgnorePublicAcls:      aws.Bool(t),
			RestrictPublicBuckets: aws.Bool(t),
		},
	})
	if err != nil {
		return &protocol.BurstSetupError{Step: "configure S3 public access block", Cause: err.Error()}
	}

	// Lifecycle rules: sessions/ expires after 7 days, images/ after 30 days
	_, err = c.client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket: aws.String(bucket),
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{
			Rules: []types.LifecycleRule{
				{
					ID:     aws.String("expire-sessions"),
					Status: types.ExpirationStatusEnabled,
					Filter: &types.LifecycleRuleFilter{Prefix: aws.String("sessions/")},
					Expiration: &types.LifecycleExpiration{
						Days: aws.Int32(7),
					},
				},
				{
					ID:     aws.String("expire-images"),
					Status: types.ExpirationStatusEnabled,
					Filter: &types.LifecycleRuleFilter{Prefix: aws.String("images/")},
					Expiration: &types.LifecycleExpiration{
						Days: aws.Int32(30),
					},
				},
			},
		},
	})
	if err != nil {
		return &protocol.BurstSetupError{Step: "configure S3 lifecycle rules", Cause: err.Error()}
	}

	return nil
}

// GetObject downloads an object and returns its contents.
func (c *S3Client) GetObject(ctx context.Context, bucket, key string) ([]byte, error) {
	out, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

// PutObject uploads data to S3.
func (c *S3Client) PutObject(ctx context.Context, bucket, key string, body []byte) error {
	_, err := c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader(string(body)),
	})
	return err
}

// DeleteObject removes a single object.
func (c *S3Client) DeleteObject(ctx context.Context, bucket, key string) error {
	_, err := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	return err
}

// ListObjects returns all object keys under the given prefix.
func (c *S3Client) ListObjects(ctx context.Context, bucket, prefix string) ([]string, error) {
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(c.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			keys = append(keys, aws.ToString(obj.Key))
		}
	}
	return keys, nil
}

// DeleteObjects deletes up to 1000 objects by key in a single request.
// For more than 1000 keys, call this function in batches.
func (c *S3Client) DeleteObjects(ctx context.Context, bucket string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	objects := make([]types.ObjectIdentifier, len(keys))
	for i, k := range keys {
		objects[i] = types.ObjectIdentifier{Key: aws.String(k)}
	}
	_, err := c.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket),
		Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(true)},
	})
	return err
}

// EmptyAndDeleteBucket empties all objects from the bucket (in 1000-key batches),
// then deletes the bucket itself. Returns nil if the bucket does not exist.
func (c *S3Client) EmptyAndDeleteBucket(ctx context.Context, bucket string) error {
	exists, err := c.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	// Delete all objects in batches of 1000
	paginator := s3.NewListObjectsV2Paginator(c.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("listing objects for deletion: %w", err)
		}
		if len(page.Contents) == 0 {
			continue
		}
		keys := make([]string, len(page.Contents))
		for i, obj := range page.Contents {
			keys[i] = aws.ToString(obj.Key)
		}
		if err := c.DeleteObjects(ctx, bucket, keys); err != nil {
			return fmt.Errorf("deleting objects: %w", err)
		}
	}

	_, err = c.client.DeleteBucket(ctx, &s3.DeleteBucketInput{
		Bucket: aws.String(bucket),
	})
	return err
}

// isS3Forbidden returns true if the error represents an HTTP 403 from S3.
func isS3Forbidden(err error) bool {
	var forbidden *types.NoSuchBucket
	if errors.As(err, &forbidden) {
		return false
	}
	return strings.Contains(err.Error(), "403") ||
		strings.Contains(err.Error(), "Forbidden") ||
		strings.Contains(err.Error(), "AccessDenied")
}

// isS3AlreadyOwnedByYou returns true if the error is BucketAlreadyOwnedByYou.
func isS3AlreadyOwnedByYou(err error) bool {
	var alreadyOwned *types.BucketAlreadyOwnedByYou
	return errors.As(err, &alreadyOwned)
}
