package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

// ec2API is a narrow interface over the ec2.Client methods used here.
type ec2API interface {
	DescribeVpcs(ctx context.Context, in *ec2.DescribeVpcsInput, opts ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(ctx context.Context, in *ec2.DescribeSubnetsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
}

// VPCClient wraps VPC/subnet/security group lookups used by burst-core.
type VPCClient struct {
	client ec2API
	region string
}

// NewVPCClient creates a VPCClient from an AWS config.
func NewVPCClient(cfg aws.Config) *VPCClient {
	return &VPCClient{
		client: ec2.NewFromConfig(cfg),
		region: cfg.Region,
	}
}

// GetDefaultVPCSubnets returns the subnet IDs of all subnets in the default VPC.
// Returns BurstSetupError if no default VPC exists.
func (c *VPCClient) GetDefaultVPCSubnets(ctx context.Context) ([]string, error) {
	vpcID, err := c.getDefaultVPCID(ctx)
	if err != nil {
		return nil, err
	}

	out, err := c.client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
			{Name: aws.String("default-for-az"), Values: []string{"true"}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("describing subnets: %w", err)
	}

	if len(out.Subnets) == 0 {
		return nil, &protocol.BurstSetupError{
			Step:        "list default VPC subnets",
			Cause:       fmt.Sprintf("no default subnets found in VPC %s", vpcID),
			Remediation: "ensure the default VPC has at least one default subnet per availability zone",
		}
	}

	subnets := make([]string, 0, len(out.Subnets))
	for _, s := range out.Subnets {
		subnets = append(subnets, aws.ToString(s.SubnetId))
	}
	return subnets, nil
}

// GetDefaultSecurityGroup returns the ID of the default security group in the default VPC.
func (c *VPCClient) GetDefaultSecurityGroup(ctx context.Context) (string, error) {
	vpcID, err := c.getDefaultVPCID(ctx)
	if err != nil {
		return "", err
	}

	out, err := c.client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
			{Name: aws.String("group-name"), Values: []string{"default"}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("describing security groups: %w", err)
	}

	if len(out.SecurityGroups) == 0 {
		return "", &protocol.BurstSetupError{
			Step:        "get default security group",
			Cause:       fmt.Sprintf("no default security group found in VPC %s", vpcID),
			Remediation: "ensure the default VPC has a default security group",
		}
	}

	return aws.ToString(out.SecurityGroups[0].GroupId), nil
}

// getDefaultVPCID returns the ID of the default VPC in the configured region.
func (c *VPCClient) getDefaultVPCID(ctx context.Context) (string, error) {
	out, err := c.client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []types.Filter{
			{Name: aws.String("is-default"), Values: []string{"true"}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("describing VPCs: %w", err)
	}

	if len(out.Vpcs) == 0 {
		return "", &protocol.BurstSetupError{
			Step:  "find default VPC",
			Cause: fmt.Sprintf("no default VPC found in region %s", c.region),
			Remediation: fmt.Sprintf(
				"create a default VPC: aws ec2 create-default-vpc --region %s", c.region,
			),
		}
	}

	return aws.ToString(out.Vpcs[0].VpcId), nil
}
