package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

type mockECR struct {
	describeRepositoriesFn  func(context.Context, *ecr.DescribeRepositoriesInput, ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error)
	createRepositoryFn      func(context.Context, *ecr.CreateRepositoryInput, ...func(*ecr.Options)) (*ecr.CreateRepositoryOutput, error)
	deleteRepositoryFn      func(context.Context, *ecr.DeleteRepositoryInput, ...func(*ecr.Options)) (*ecr.DeleteRepositoryOutput, error)
	describeImagesFn        func(context.Context, *ecr.DescribeImagesInput, ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error)
	getAuthorizationTokenFn func(context.Context, *ecr.GetAuthorizationTokenInput, ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error)
	batchDeleteImageFn      func(context.Context, *ecr.BatchDeleteImageInput, ...func(*ecr.Options)) (*ecr.BatchDeleteImageOutput, error)
}

func (m *mockECR) DescribeRepositories(ctx context.Context, in *ecr.DescribeRepositoriesInput, opts ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error) {
	return m.describeRepositoriesFn(ctx, in, opts...)
}
func (m *mockECR) CreateRepository(ctx context.Context, in *ecr.CreateRepositoryInput, opts ...func(*ecr.Options)) (*ecr.CreateRepositoryOutput, error) {
	return m.createRepositoryFn(ctx, in, opts...)
}
func (m *mockECR) DeleteRepository(ctx context.Context, in *ecr.DeleteRepositoryInput, opts ...func(*ecr.Options)) (*ecr.DeleteRepositoryOutput, error) {
	if m.deleteRepositoryFn != nil {
		return m.deleteRepositoryFn(ctx, in, opts...)
	}
	return &ecr.DeleteRepositoryOutput{}, nil
}
func (m *mockECR) DescribeImages(ctx context.Context, in *ecr.DescribeImagesInput, opts ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
	return m.describeImagesFn(ctx, in, opts...)
}
func (m *mockECR) GetAuthorizationToken(ctx context.Context, in *ecr.GetAuthorizationTokenInput, opts ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	return m.getAuthorizationTokenFn(ctx, in, opts...)
}
func (m *mockECR) BatchDeleteImage(ctx context.Context, in *ecr.BatchDeleteImageInput, opts ...func(*ecr.Options)) (*ecr.BatchDeleteImageOutput, error) {
	if m.batchDeleteImageFn != nil {
		return m.batchDeleteImageFn(ctx, in, opts...)
	}
	return &ecr.BatchDeleteImageOutput{}, nil
}

func TestCreateRepository_alreadyExists(t *testing.T) {
	const uri = "123.dkr.ecr.us-east-1.amazonaws.com/burst-workers-python"
	c := &ECRClient{client: &mockECR{
		describeRepositoriesFn: func(_ context.Context, _ *ecr.DescribeRepositoriesInput, _ ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error) {
			return &ecr.DescribeRepositoriesOutput{
				Repositories: []types.Repository{
					{RepositoryUri: aws.String(uri)},
				},
			}, nil
		},
		createRepositoryFn: func(_ context.Context, _ *ecr.CreateRepositoryInput, _ ...func(*ecr.Options)) (*ecr.CreateRepositoryOutput, error) {
			t.Fatal("CreateRepository should not be called")
			return nil, nil
		},
	}, accountID: "123", region: "us-east-1"}

	got, err := c.CreateRepository(context.Background(), "burst-workers-python")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != uri {
		t.Errorf("got %q, want %q", got, uri)
	}
}

func TestCreateRepository_new(t *testing.T) {
	const uri = "123.dkr.ecr.us-east-1.amazonaws.com/burst-workers-go"
	c := &ECRClient{client: &mockECR{
		describeRepositoriesFn: func(_ context.Context, _ *ecr.DescribeRepositoriesInput, _ ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error) {
			return nil, &types.RepositoryNotFoundException{}
		},
		createRepositoryFn: func(_ context.Context, _ *ecr.CreateRepositoryInput, _ ...func(*ecr.Options)) (*ecr.CreateRepositoryOutput, error) {
			return &ecr.CreateRepositoryOutput{
				Repository: &types.Repository{RepositoryUri: aws.String(uri)},
			}, nil
		},
	}, accountID: "123", region: "us-east-1"}

	got, err := c.CreateRepository(context.Background(), "burst-workers-go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != uri {
		t.Errorf("got %q, want %q", got, uri)
	}
}

func TestCreateRepository_createFails(t *testing.T) {
	c := &ECRClient{client: &mockECR{
		describeRepositoriesFn: func(_ context.Context, _ *ecr.DescribeRepositoriesInput, _ ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error) {
			return nil, &types.RepositoryNotFoundException{}
		},
		createRepositoryFn: func(_ context.Context, _ *ecr.CreateRepositoryInput, _ ...func(*ecr.Options)) (*ecr.CreateRepositoryOutput, error) {
			return nil, errors.New("AccessDenied")
		},
	}, accountID: "123", region: "us-east-1"}

	_, err := c.CreateRepository(context.Background(), "burst-workers-go")
	var setupErr *protocol.BurstSetupError
	if !errors.As(err, &setupErr) {
		t.Errorf("expected BurstSetupError, got %T: %v", err, err)
	}
}

func TestImageExists_true(t *testing.T) {
	c := &ECRClient{client: &mockECR{
		describeImagesFn: func(_ context.Context, _ *ecr.DescribeImagesInput, _ ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
			return &ecr.DescribeImagesOutput{
				ImageDetails: []types.ImageDetail{{ImageTags: []string{"sha256:abc123"}}},
			}, nil
		},
	}, accountID: "123", region: "us-east-1"}

	exists, err := c.ImageExists(context.Background(), "burst-workers-python", "sha256:abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected image to exist")
	}
}

func TestImageExists_false(t *testing.T) {
	c := &ECRClient{client: &mockECR{
		describeImagesFn: func(_ context.Context, _ *ecr.DescribeImagesInput, _ ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
			return nil, &types.ImageNotFoundException{}
		},
	}, accountID: "123", region: "us-east-1"}

	exists, err := c.ImageExists(context.Background(), "burst-workers-python", "sha256:missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected image to not exist")
	}
}

func TestBaseURI(t *testing.T) {
	c := &ECRClient{accountID: "123456789012", region: "us-west-2"}
	want := "123456789012.dkr.ecr.us-west-2.amazonaws.com"
	if got := c.BaseURI(); got != want {
		t.Errorf("BaseURI() = %q, want %q", got, want)
	}
}
