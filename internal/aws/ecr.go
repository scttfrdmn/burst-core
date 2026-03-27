package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// ecrAPI is a narrow interface over the ecr.Client methods used here.
type ecrAPI interface {
	DescribeRepositories(ctx context.Context, in *ecr.DescribeRepositoriesInput, opts ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error)
	CreateRepository(ctx context.Context, in *ecr.CreateRepositoryInput, opts ...func(*ecr.Options)) (*ecr.CreateRepositoryOutput, error)
	DescribeImages(ctx context.Context, in *ecr.DescribeImagesInput, opts ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error)
	GetAuthorizationToken(ctx context.Context, in *ecr.GetAuthorizationTokenInput, opts ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error)
	BatchDeleteImage(ctx context.Context, in *ecr.BatchDeleteImageInput, opts ...func(*ecr.Options)) (*ecr.BatchDeleteImageOutput, error)
}

// ECRClient wraps ECR operations used by burst-core.
type ECRClient struct {
	client    ecrAPI
	accountID string
	region    string
}

// NewECRClient creates an ECRClient from an AWS config.
func NewECRClient(cfg aws.Config, accountID string) *ECRClient {
	return &ECRClient{
		client:    ecr.NewFromConfig(cfg),
		accountID: accountID,
		region:    cfg.Region,
	}
}

// BaseURI returns the ECR base URI for this account/region.
// e.g. "123456789012.dkr.ecr.us-east-1.amazonaws.com"
func (c *ECRClient) BaseURI() string {
	return fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com", c.accountID, c.region)
}

// CreateRepository creates an ECR repository idempotently.
// Returns the repository URI.
func (c *ECRClient) CreateRepository(ctx context.Context, name string) (string, error) {
	// Check if already exists
	out, err := c.client.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{
		RepositoryNames: []string{name},
	})
	if err == nil && len(out.Repositories) > 0 {
		return aws.ToString(out.Repositories[0].RepositoryUri), nil
	}

	var notFound *types.RepositoryNotFoundException
	if err != nil && !errors.As(err, &notFound) {
		return "", &protocol.BurstSetupError{
			Step:  "check ECR repository",
			Cause: err.Error(),
		}
	}

	createOut, err := c.client.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName:     aws.String(name),
		ImageTagMutability: types.ImageTagMutabilityMutable,
		ImageScanningConfiguration: &types.ImageScanningConfiguration{
			ScanOnPush: false,
		},
		Tags: []types.Tag{
			{Key: aws.String("managed-by"), Value: aws.String("burst-core")},
		},
	})
	if err != nil {
		return "", &protocol.BurstSetupError{
			Step:        "create ECR repository",
			Cause:       err.Error(),
			Remediation: "ensure your AWS identity has ecr:CreateRepository permission",
		}
	}

	return aws.ToString(createOut.Repository.RepositoryUri), nil
}

// ImageExists returns true if an image with the given tag exists in the repository.
func (c *ECRClient) ImageExists(ctx context.Context, repoName, tag string) (bool, error) {
	_, err := c.client.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String(repoName),
		ImageIds: []types.ImageIdentifier{
			{ImageTag: aws.String(tag)},
		},
	})
	if err == nil {
		return true, nil
	}

	var notFound *types.ImageNotFoundException
	if errors.As(err, &notFound) {
		return false, nil
	}

	var repoNotFound *types.RepositoryNotFoundException
	if errors.As(err, &repoNotFound) {
		return false, nil
	}

	return false, err
}

// AuthToken returns a Docker-compatible auth token for the ECR registry.
// The returned string is in "username:password" format, suitable for
// `docker login --username AWS --password <token>`.
func (c *ECRClient) AuthToken(ctx context.Context) (string, error) {
	out, err := c.client.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return "", fmt.Errorf("getting ECR auth token: %w", err)
	}
	if len(out.AuthorizationData) == 0 {
		return "", fmt.Errorf("no ECR authorization data returned")
	}

	encoded := aws.ToString(out.AuthorizationData[0].AuthorizationToken)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decoding ECR auth token: %w", err)
	}

	// token is "AWS:password" — return just the password part
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("unexpected ECR auth token format")
	}
	return parts[1], nil
}
