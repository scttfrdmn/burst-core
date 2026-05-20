package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

type mockEC2 struct {
	describeVpcsFn           func(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	describeSubnetsFn        func(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	describeSecurityGroupsFn func(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
}

func (m *mockEC2) DescribeVpcs(ctx context.Context, in *ec2.DescribeVpcsInput, opts ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return m.describeVpcsFn(ctx, in, opts...)
}
func (m *mockEC2) DescribeSubnets(ctx context.Context, in *ec2.DescribeSubnetsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	return m.describeSubnetsFn(ctx, in, opts...)
}
func (m *mockEC2) DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return m.describeSecurityGroupsFn(ctx, in, opts...)
}

func defaultVPCMock(vpcID string) func(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return func(_ context.Context, _ *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
		return &ec2.DescribeVpcsOutput{
			Vpcs: []types.Vpc{{VpcId: aws.String(vpcID)}},
		}, nil
	}
}

func TestGetDefaultVPCSubnets(t *testing.T) {
	c := &VPCClient{client: &mockEC2{
		describeVpcsFn: defaultVPCMock("vpc-12345"),
		describeSubnetsFn: func(_ context.Context, _ *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
			return &ec2.DescribeSubnetsOutput{
				Subnets: []types.Subnet{
					{SubnetId: aws.String("subnet-aaa")},
					{SubnetId: aws.String("subnet-bbb")},
				},
			}, nil
		},
	}, region: "us-east-1"}

	subnets, err := c.GetDefaultVPCSubnets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(subnets) != 2 {
		t.Errorf("got %d subnets, want 2", len(subnets))
	}
}

func TestGetDefaultVPCSubnets_noVPC(t *testing.T) {
	c := &VPCClient{client: &mockEC2{
		describeVpcsFn: func(_ context.Context, _ *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
			return &ec2.DescribeVpcsOutput{Vpcs: []types.Vpc{}}, nil
		},
	}, region: "us-east-1"}

	_, err := c.GetDefaultVPCSubnets(context.Background())
	var setupErr *protocol.BurstSetupError
	if !errors.As(err, &setupErr) {
		t.Errorf("expected BurstSetupError, got %T: %v", err, err)
	}
	if setupErr.Remediation == "" {
		t.Error("expected Remediation hint to be set")
	}
}

func TestGetDefaultSecurityGroup(t *testing.T) {
	c := &VPCClient{client: &mockEC2{
		describeVpcsFn: defaultVPCMock("vpc-12345"),
		describeSecurityGroupsFn: func(_ context.Context, _ *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
			return &ec2.DescribeSecurityGroupsOutput{
				SecurityGroups: []types.SecurityGroup{
					{GroupId: aws.String("sg-default123")},
				},
			}, nil
		},
	}, region: "us-east-1"}

	sg, err := c.GetDefaultSecurityGroup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sg != "sg-default123" {
		t.Errorf("got %q, want %q", sg, "sg-default123")
	}
}
