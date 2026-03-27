package aws

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// mockS3 is a hand-written mock implementing s3API.
type mockS3 struct {
	headBucketFn               func(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	createBucketFn             func(context.Context, *s3.CreateBucketInput, ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
	putBucketEncryptionFn      func(context.Context, *s3.PutBucketEncryptionInput, ...func(*s3.Options)) (*s3.PutBucketEncryptionOutput, error)
	putPublicAccessBlockFn     func(context.Context, *s3.PutPublicAccessBlockInput, ...func(*s3.Options)) (*s3.PutPublicAccessBlockOutput, error)
	putBucketLifecycleFn       func(context.Context, *s3.PutBucketLifecycleConfigurationInput, ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error)
	getObjectFn                func(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	putObjectFn                func(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	deleteObjectFn             func(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	deleteObjectsFn            func(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
	listObjectsV2Fn            func(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	deleteBucketFn             func(context.Context, *s3.DeleteBucketInput, ...func(*s3.Options)) (*s3.DeleteBucketOutput, error)
}

func (m *mockS3) HeadBucket(ctx context.Context, in *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return m.headBucketFn(ctx, in, opts...)
}
func (m *mockS3) CreateBucket(ctx context.Context, in *s3.CreateBucketInput, opts ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	return m.createBucketFn(ctx, in, opts...)
}
func (m *mockS3) PutBucketEncryption(ctx context.Context, in *s3.PutBucketEncryptionInput, opts ...func(*s3.Options)) (*s3.PutBucketEncryptionOutput, error) {
	if m.putBucketEncryptionFn != nil {
		return m.putBucketEncryptionFn(ctx, in, opts...)
	}
	return &s3.PutBucketEncryptionOutput{}, nil
}
func (m *mockS3) PutPublicAccessBlock(ctx context.Context, in *s3.PutPublicAccessBlockInput, opts ...func(*s3.Options)) (*s3.PutPublicAccessBlockOutput, error) {
	if m.putPublicAccessBlockFn != nil {
		return m.putPublicAccessBlockFn(ctx, in, opts...)
	}
	return &s3.PutPublicAccessBlockOutput{}, nil
}
func (m *mockS3) PutBucketLifecycleConfiguration(ctx context.Context, in *s3.PutBucketLifecycleConfigurationInput, opts ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error) {
	if m.putBucketLifecycleFn != nil {
		return m.putBucketLifecycleFn(ctx, in, opts...)
	}
	return &s3.PutBucketLifecycleConfigurationOutput{}, nil
}
func (m *mockS3) GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return m.getObjectFn(ctx, in, opts...)
}
func (m *mockS3) PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return m.putObjectFn(ctx, in, opts...)
}
func (m *mockS3) DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return m.deleteObjectFn(ctx, in, opts...)
}
func (m *mockS3) DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, opts ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	if m.deleteObjectsFn != nil {
		return m.deleteObjectsFn(ctx, in, opts...)
	}
	return &s3.DeleteObjectsOutput{}, nil
}
func (m *mockS3) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return m.listObjectsV2Fn(ctx, in, opts...)
}
func (m *mockS3) DeleteBucket(ctx context.Context, in *s3.DeleteBucketInput, opts ...func(*s3.Options)) (*s3.DeleteBucketOutput, error) {
	if m.deleteBucketFn != nil {
		return m.deleteBucketFn(ctx, in, opts...)
	}
	return &s3.DeleteBucketOutput{}, nil
}

func TestBucketExists_true(t *testing.T) {
	c := &S3Client{client: &mockS3{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
	}, region: "us-east-1"}

	exists, err := c.BucketExists(context.Background(), "my-bucket")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected bucket to exist")
	}
}

func TestBucketExists_false(t *testing.T) {
	c := &S3Client{client: &mockS3{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return nil, &types.NotFound{}
		},
	}, region: "us-east-1"}

	exists, err := c.BucketExists(context.Background(), "missing-bucket")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected bucket to not exist")
	}
}

func TestBucketExists_foreignOwner(t *testing.T) {
	c := &S3Client{client: &mockS3{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return nil, errors.New("403 Forbidden: AccessDenied")
		},
	}, region: "us-east-1"}

	_, err := c.BucketExists(context.Background(), "someone-elses-bucket")
	var setupErr *protocol.BurstSetupError
	if !errors.As(err, &setupErr) {
		t.Errorf("expected BurstSetupError, got %T: %v", err, err)
	}
}

func TestCreateBucket_alreadyExists(t *testing.T) {
	calls := 0
	c := &S3Client{client: &mockS3{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			calls++
			return &s3.HeadBucketOutput{}, nil // exists
		},
		createBucketFn: func(_ context.Context, _ *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
			t.Fatal("CreateBucket should not be called if bucket exists")
			return nil, nil
		},
	}, region: "us-east-1"}

	if err := c.CreateBucket(context.Background(), "my-bucket"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 HeadBucket call, got %d", calls)
	}
}

func TestCreateBucket_usEast1NoConstraint(t *testing.T) {
	var got *s3.CreateBucketInput
	c := &S3Client{client: &mockS3{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return nil, &types.NotFound{}
		},
		createBucketFn: func(_ context.Context, in *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
			got = in
			return &s3.CreateBucketOutput{}, nil
		},
	}, region: "us-east-1"}

	if err := c.CreateBucket(context.Background(), "my-bucket"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CreateBucketConfiguration != nil {
		t.Error("us-east-1 must not send CreateBucketConfiguration (LocationConstraint)")
	}
}

func TestCreateBucket_nonUsEast1HasConstraint(t *testing.T) {
	var got *s3.CreateBucketInput
	c := &S3Client{client: &mockS3{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return nil, &types.NotFound{}
		},
		createBucketFn: func(_ context.Context, in *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
			got = in
			return &s3.CreateBucketOutput{}, nil
		},
	}, region: "us-west-2"}

	if err := c.CreateBucket(context.Background(), "my-bucket"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CreateBucketConfiguration == nil {
		t.Error("non-us-east-1 must send CreateBucketConfiguration with LocationConstraint")
	}
}

func TestPutObject(t *testing.T) {
	var gotKey string
	var gotBody string
	c := &S3Client{client: &mockS3{
		putObjectFn: func(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
			gotKey = aws.ToString(in.Key)
			b, _ := io.ReadAll(in.Body)
			gotBody = string(b)
			return &s3.PutObjectOutput{}, nil
		},
	}, region: "us-east-1"}

	if err := c.PutObject(context.Background(), "bucket", "key/file.json", []byte(`{"hello":"world"}`)); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if gotKey != "key/file.json" {
		t.Errorf("key: got %q want %q", gotKey, "key/file.json")
	}
	if !strings.Contains(gotBody, "hello") {
		t.Errorf("body: got %q, want to contain \"hello\"", gotBody)
	}
}
