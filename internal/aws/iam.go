package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// iamAPI is a narrow interface over the iam.Client methods used here.
type iamAPI interface {
	GetRole(ctx context.Context, in *iam.GetRoleInput, opts ...func(*iam.Options)) (*iam.GetRoleOutput, error)
	CreateRole(ctx context.Context, in *iam.CreateRoleInput, opts ...func(*iam.Options)) (*iam.CreateRoleOutput, error)
	AttachRolePolicy(ctx context.Context, in *iam.AttachRolePolicyInput, opts ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error)
	PutRolePolicy(ctx context.Context, in *iam.PutRolePolicyInput, opts ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error)
	DeleteRole(ctx context.Context, in *iam.DeleteRoleInput, opts ...func(*iam.Options)) (*iam.DeleteRoleOutput, error)
	DetachRolePolicy(ctx context.Context, in *iam.DetachRolePolicyInput, opts ...func(*iam.Options)) (*iam.DetachRolePolicyOutput, error)
	DeleteRolePolicy(ctx context.Context, in *iam.DeleteRolePolicyInput, opts ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error)
	ListAttachedRolePolicies(ctx context.Context, in *iam.ListAttachedRolePoliciesInput, opts ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error)
	ListRolePolicies(ctx context.Context, in *iam.ListRolePoliciesInput, opts ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error)
}

// IAMClient wraps IAM operations used by burst-core setup/teardown.
type IAMClient struct {
	client    iamAPI
	accountID string
	region    string
}

// NewIAMClient creates an IAMClient from an AWS config.
func NewIAMClient(cfg aws.Config, accountID string) *IAMClient {
	return &IAMClient{
		client:    iam.NewFromConfig(cfg),
		accountID: accountID,
		region:    cfg.Region,
	}
}

// ecsTrustPolicy is the trust relationship allowing ECS tasks to assume the role.
const ecsTrustPolicy = `{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Service": "ecs-tasks.amazonaws.com"},
    "Action": "sts:AssumeRole"
  }]
}`

// CreateExecutionRole creates burst-execution-role idempotently.
// Returns the role ARN.
func (c *IAMClient) CreateExecutionRole(ctx context.Context) (string, error) {
	const roleName = "burst-execution-role"

	// Check if already exists
	out, err := c.client.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
	if err == nil {
		return aws.ToString(out.Role.Arn), nil
	}
	if !isIAMNoSuchEntity(err) {
		return "", &protocol.BurstSetupError{
			Step:  "check IAM execution role",
			Cause: err.Error(),
		}
	}

	// Create the role
	createOut, err := c.client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(ecsTrustPolicy),
		Description:              aws.String("burst-core ECS task execution role"),
		Tags: []types.Tag{
			{Key: aws.String("managed-by"), Value: aws.String("burst-core")},
		},
	})
	if err != nil {
		return "", &protocol.BurstSetupError{
			Step:        "create IAM execution role",
			Cause:       err.Error(),
			Remediation: "ensure your AWS identity has iam:CreateRole permission",
		}
	}

	// Attach managed ECS execution policy
	_, err = c.client.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
		RoleName:  aws.String(roleName),
		PolicyArn: aws.String("arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"),
	})
	if err != nil {
		return "", &protocol.BurstSetupError{
			Step:  "attach ECS execution policy to execution role",
			Cause: err.Error(),
		}
	}

	// Inline ECR pull policy for burst-workers-* repos
	ecrPolicy := c.ecrPullPolicy()
	_, err = c.client.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String("burst-ecr-pull"),
		PolicyDocument: aws.String(ecrPolicy),
	})
	if err != nil {
		return "", &protocol.BurstSetupError{Step: "attach ECR pull policy to execution role", Cause: err.Error()}
	}

	return aws.ToString(createOut.Role.Arn), nil
}

// CreateTaskRole creates burst-task-role idempotently.
// Returns the role ARN.
func (c *IAMClient) CreateTaskRole(ctx context.Context, bucket string) (string, error) {
	const roleName = "burst-task-role"

	out, err := c.client.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
	if err == nil {
		return aws.ToString(out.Role.Arn), nil
	}
	if !isIAMNoSuchEntity(err) {
		return "", &protocol.BurstSetupError{Step: "check IAM task role", Cause: err.Error()}
	}

	createOut, err := c.client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(ecsTrustPolicy),
		Description:              aws.String("burst-core ECS task role (S3 access)"),
		Tags: []types.Tag{
			{Key: aws.String("managed-by"), Value: aws.String("burst-core")},
		},
	})
	if err != nil {
		return "", &protocol.BurstSetupError{
			Step:        "create IAM task role",
			Cause:       err.Error(),
			Remediation: "ensure your AWS identity has iam:CreateRole permission",
		}
	}

	// Inline S3 policy scoped to the burst bucket
	s3Policy := c.s3AccessPolicy(bucket)
	_, err = c.client.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String("burst-s3-access"),
		PolicyDocument: aws.String(s3Policy),
	})
	if err != nil {
		return "", &protocol.BurstSetupError{Step: "attach S3 policy to task role", Cause: err.Error()}
	}

	return aws.ToString(createOut.Role.Arn), nil
}

// ecrPullPolicy returns an inline policy allowing ECR pulls on burst-workers-* repos.
func (c *IAMClient) ecrPullPolicy() string {
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Effect": "Allow",
				"Action": []string{
					"ecr:GetDownloadUrlForLayer",
					"ecr:BatchGetImage",
					"ecr:BatchCheckLayerAvailability",
				},
				"Resource": fmt.Sprintf(
					"arn:aws:ecr:%s:%s:repository/burst-workers-*",
					c.region, c.accountID,
				),
			},
			{
				"Effect":   "Allow",
				"Action":   "ecr:GetAuthorizationToken",
				"Resource": "*",
			},
		},
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

// s3AccessPolicy returns an inline policy allowing S3 CRUD on the burst bucket.
func (c *IAMClient) s3AccessPolicy(bucket string) string {
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Effect": "Allow",
				"Action": []string{
					"s3:GetObject",
					"s3:PutObject",
					"s3:DeleteObject",
				},
				"Resource": fmt.Sprintf("arn:aws:s3:::%s/*", bucket),
			},
			{
				"Effect":   "Allow",
				"Action":   "s3:ListBucket",
				"Resource": fmt.Sprintf("arn:aws:s3:::%s", bucket),
			},
		},
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

// RoleExists returns true if the named IAM role exists.
func (c *IAMClient) RoleExists(ctx context.Context, roleName string) (bool, error) {
	_, err := c.client.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
	if err == nil {
		return true, nil
	}
	if isIAMNoSuchEntity(err) {
		return false, nil
	}
	return false, err
}

// DeleteRole detaches all managed policies, deletes all inline policies, then
// deletes the named IAM role. Safe to call on a non-existent role (returns nil).
func (c *IAMClient) DeleteRole(ctx context.Context, roleName string) error {
	// Confirm role exists
	exists, err := c.RoleExists(ctx, roleName)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	// Detach all managed policies
	attached, err := c.client.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
		RoleName: aws.String(roleName),
	})
	if err != nil {
		return fmt.Errorf("listing attached policies for %q: %w", roleName, err)
	}
	for _, p := range attached.AttachedPolicies {
		if _, err := c.client.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{
			RoleName:  aws.String(roleName),
			PolicyArn: p.PolicyArn,
		}); err != nil {
			return fmt.Errorf("detaching policy %q from %q: %w", aws.ToString(p.PolicyArn), roleName, err)
		}
	}

	// Delete all inline policies
	inline, err := c.client.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{
		RoleName: aws.String(roleName),
	})
	if err != nil {
		return fmt.Errorf("listing inline policies for %q: %w", roleName, err)
	}
	for _, name := range inline.PolicyNames {
		if _, err := c.client.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
			RoleName:   aws.String(roleName),
			PolicyName: aws.String(name),
		}); err != nil {
			return fmt.Errorf("deleting inline policy %q from %q: %w", name, roleName, err)
		}
	}

	// Delete the role itself
	if _, err := c.client.DeleteRole(ctx, &iam.DeleteRoleInput{
		RoleName: aws.String(roleName),
	}); err != nil {
		return fmt.Errorf("deleting role %q: %w", roleName, err)
	}
	return nil
}

// isIAMNoSuchEntity returns true if the IAM error is NoSuchEntityException.
func isIAMNoSuchEntity(err error) bool {
	var nse *types.NoSuchEntityException
	return errors.As(err, &nse)
}
