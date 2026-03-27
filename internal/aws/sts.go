package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// stsAPI is a narrow interface over the sts.Client methods used here.
type stsAPI interface {
	GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, opts ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// STSClient wraps STS operations used by burst-core.
type STSClient struct {
	client stsAPI
	region string
}

// NewSTSClient creates an STSClient from an AWS config.
func NewSTSClient(cfg aws.Config) *STSClient {
	return &STSClient{client: sts.NewFromConfig(cfg), region: cfg.Region}
}

// CallerIdentity holds the result of GetCallerIdentity.
type CallerIdentity struct {
	AccountID string
	ARN       string
	UserID    string
}

// GetCallerIdentity validates AWS credentials and returns account details.
// Returns BurstSetupError if credentials are invalid or not configured.
func (c *STSClient) GetCallerIdentity(ctx context.Context) (*CallerIdentity, error) {
	out, err := c.client.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, &protocol.BurstSetupError{
			Step:        "validate AWS credentials",
			Cause:       err.Error(),
			Remediation: "run: aws configure",
		}
	}
	return &CallerIdentity{
		AccountID: aws.ToString(out.Account),
		ARN:       aws.ToString(out.Arn),
		UserID:    aws.ToString(out.UserId),
	}, nil
}
