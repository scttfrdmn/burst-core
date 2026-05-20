package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// ImageDetail holds metadata about a single ECR image.
type ImageDetail struct {
	Tags      []string
	Digest    string
	PushedAt  time.Time
	SizeBytes int64
}

// ecrAPI is a narrow interface over the ecr.Client methods used here.
type ecrAPI interface {
	DescribeRepositories(ctx context.Context, in *ecr.DescribeRepositoriesInput, opts ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error)
	CreateRepository(ctx context.Context, in *ecr.CreateRepositoryInput, opts ...func(*ecr.Options)) (*ecr.CreateRepositoryOutput, error)
	DeleteRepository(ctx context.Context, in *ecr.DeleteRepositoryInput, opts ...func(*ecr.Options)) (*ecr.DeleteRepositoryOutput, error)
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

// DeleteRepository force-deletes an ECR repository including all images.
// Returns nil if the repository does not exist.
func (c *ECRClient) DeleteRepository(ctx context.Context, name string) error {
	_, err := c.client.DeleteRepository(ctx, &ecr.DeleteRepositoryInput{
		RepositoryName: aws.String(name),
		Force:          true,
	})
	if err == nil {
		return nil
	}
	var notFound *types.RepositoryNotFoundException
	if errors.As(err, &notFound) {
		return nil
	}
	return fmt.Errorf("deleting ECR repository %q: %w", name, err)
}

// ListBurstRepositories returns all ECR repository names starting with "burst-workers-".
func (c *ECRClient) ListBurstRepositories(ctx context.Context) ([]string, error) {
	var names []string
	var nextToken *string
	for {
		out, err := c.client.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{
			NextToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("listing ECR repositories: %w", err)
		}
		for _, r := range out.Repositories {
			name := aws.ToString(r.RepositoryName)
			if strings.HasPrefix(name, "burst-workers-") {
				names = append(names, name)
			}
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	return names, nil
}

// ListImages returns details for all images in the named repository.
func (c *ECRClient) ListImages(ctx context.Context, repoName string) ([]ImageDetail, error) {
	var details []ImageDetail
	var nextToken *string
	for {
		out, err := c.client.DescribeImages(ctx, &ecr.DescribeImagesInput{
			RepositoryName: aws.String(repoName),
			NextToken:      nextToken,
		})
		if err != nil {
			var repoNotFound *types.RepositoryNotFoundException
			if errors.As(err, &repoNotFound) {
				return nil, nil
			}
			return nil, fmt.Errorf("listing images in %q: %w", repoName, err)
		}
		for _, img := range out.ImageDetails {
			d := ImageDetail{
				Tags:      img.ImageTags,
				Digest:    aws.ToString(img.ImageDigest),
				SizeBytes: aws.ToInt64(img.ImageSizeInBytes),
			}
			if img.ImagePushedAt != nil {
				d.PushedAt = aws.ToTime(img.ImagePushedAt)
			}
			details = append(details, d)
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	return details, nil
}

// DeleteImages batch-deletes images by digest in batches of 100.
func (c *ECRClient) DeleteImages(ctx context.Context, repoName string, digests []string) error {
	const batchSize = 100
	for i := 0; i < len(digests); i += batchSize {
		end := i + batchSize
		if end > len(digests) {
			end = len(digests)
		}
		batch := digests[i:end]
		ids := make([]types.ImageIdentifier, len(batch))
		for j, d := range batch {
			ids[j] = types.ImageIdentifier{ImageDigest: aws.String(d)}
		}
		_, err := c.client.BatchDeleteImage(ctx, &ecr.BatchDeleteImageInput{
			RepositoryName: aws.String(repoName),
			ImageIds:       ids,
		})
		if err != nil {
			return fmt.Errorf("deleting images from %q: %w", repoName, err)
		}
	}
	return nil
}

// ImageCount returns the number of images in the named repository.
func (c *ECRClient) ImageCount(ctx context.Context, repoName string) (int, error) {
	out, err := c.client.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String(repoName),
	})
	if err != nil {
		var repoNotFound *types.RepositoryNotFoundException
		if errors.As(err, &repoNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("describing images in %q: %w", repoName, err)
	}
	return len(out.ImageDetails), nil
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
