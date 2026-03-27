package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

type mockIAM struct {
	getRoleFn                  func(context.Context, *iam.GetRoleInput, ...func(*iam.Options)) (*iam.GetRoleOutput, error)
	createRoleFn               func(context.Context, *iam.CreateRoleInput, ...func(*iam.Options)) (*iam.CreateRoleOutput, error)
	attachRolePolicyFn         func(context.Context, *iam.AttachRolePolicyInput, ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error)
	putRolePolicyFn            func(context.Context, *iam.PutRolePolicyInput, ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error)
	deleteRoleFn               func(context.Context, *iam.DeleteRoleInput, ...func(*iam.Options)) (*iam.DeleteRoleOutput, error)
	detachRolePolicyFn         func(context.Context, *iam.DetachRolePolicyInput, ...func(*iam.Options)) (*iam.DetachRolePolicyOutput, error)
	deleteRolePolicyFn         func(context.Context, *iam.DeleteRolePolicyInput, ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error)
	listAttachedRolePoliciesFn func(context.Context, *iam.ListAttachedRolePoliciesInput, ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error)
	listRolePoliciesFn         func(context.Context, *iam.ListRolePoliciesInput, ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error)
}

func (m *mockIAM) GetRole(ctx context.Context, in *iam.GetRoleInput, opts ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	return m.getRoleFn(ctx, in, opts...)
}
func (m *mockIAM) CreateRole(ctx context.Context, in *iam.CreateRoleInput, opts ...func(*iam.Options)) (*iam.CreateRoleOutput, error) {
	return m.createRoleFn(ctx, in, opts...)
}
func (m *mockIAM) AttachRolePolicy(ctx context.Context, in *iam.AttachRolePolicyInput, opts ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error) {
	if m.attachRolePolicyFn != nil {
		return m.attachRolePolicyFn(ctx, in, opts...)
	}
	return &iam.AttachRolePolicyOutput{}, nil
}
func (m *mockIAM) PutRolePolicy(ctx context.Context, in *iam.PutRolePolicyInput, opts ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error) {
	if m.putRolePolicyFn != nil {
		return m.putRolePolicyFn(ctx, in, opts...)
	}
	return &iam.PutRolePolicyOutput{}, nil
}
func (m *mockIAM) DeleteRole(ctx context.Context, in *iam.DeleteRoleInput, opts ...func(*iam.Options)) (*iam.DeleteRoleOutput, error) {
	if m.deleteRoleFn != nil {
		return m.deleteRoleFn(ctx, in, opts...)
	}
	return &iam.DeleteRoleOutput{}, nil
}
func (m *mockIAM) DetachRolePolicy(ctx context.Context, in *iam.DetachRolePolicyInput, opts ...func(*iam.Options)) (*iam.DetachRolePolicyOutput, error) {
	if m.detachRolePolicyFn != nil {
		return m.detachRolePolicyFn(ctx, in, opts...)
	}
	return &iam.DetachRolePolicyOutput{}, nil
}
func (m *mockIAM) DeleteRolePolicy(ctx context.Context, in *iam.DeleteRolePolicyInput, opts ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error) {
	if m.deleteRolePolicyFn != nil {
		return m.deleteRolePolicyFn(ctx, in, opts...)
	}
	return &iam.DeleteRolePolicyOutput{}, nil
}
func (m *mockIAM) ListAttachedRolePolicies(ctx context.Context, in *iam.ListAttachedRolePoliciesInput, opts ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error) {
	if m.listAttachedRolePoliciesFn != nil {
		return m.listAttachedRolePoliciesFn(ctx, in, opts...)
	}
	return &iam.ListAttachedRolePoliciesOutput{}, nil
}
func (m *mockIAM) ListRolePolicies(ctx context.Context, in *iam.ListRolePoliciesInput, opts ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error) {
	if m.listRolePoliciesFn != nil {
		return m.listRolePoliciesFn(ctx, in, opts...)
	}
	return &iam.ListRolePoliciesOutput{}, nil
}

func TestCreateExecutionRole_alreadyExists(t *testing.T) {
	const arn = "arn:aws:iam::123:role/burst-execution-role"
	c := &IAMClient{client: &mockIAM{
		getRoleFn: func(_ context.Context, _ *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
			return &iam.GetRoleOutput{
				Role: &types.Role{Arn: aws.String(arn)},
			}, nil
		},
		createRoleFn: func(_ context.Context, _ *iam.CreateRoleInput, _ ...func(*iam.Options)) (*iam.CreateRoleOutput, error) {
			t.Fatal("CreateRole should not be called if role exists")
			return nil, nil
		},
	}, accountID: "123", region: "us-east-1"}

	got, err := c.CreateExecutionRole(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != arn {
		t.Errorf("got ARN %q, want %q", got, arn)
	}
}

func TestCreateExecutionRole_new(t *testing.T) {
	const arn = "arn:aws:iam::123:role/burst-execution-role"
	c := &IAMClient{client: &mockIAM{
		getRoleFn: func(_ context.Context, _ *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
			return nil, &types.NoSuchEntityException{}
		},
		createRoleFn: func(_ context.Context, in *iam.CreateRoleInput, _ ...func(*iam.Options)) (*iam.CreateRoleOutput, error) {
			return &iam.CreateRoleOutput{
				Role: &types.Role{Arn: aws.String(arn)},
			}, nil
		},
	}, accountID: "123", region: "us-east-1"}

	got, err := c.CreateExecutionRole(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != arn {
		t.Errorf("got ARN %q, want %q", got, arn)
	}
}

func TestCreateExecutionRole_createFails(t *testing.T) {
	c := &IAMClient{client: &mockIAM{
		getRoleFn: func(_ context.Context, _ *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
			return nil, &types.NoSuchEntityException{}
		},
		createRoleFn: func(_ context.Context, _ *iam.CreateRoleInput, _ ...func(*iam.Options)) (*iam.CreateRoleOutput, error) {
			return nil, errors.New("AccessDenied")
		},
	}, accountID: "123", region: "us-east-1"}

	_, err := c.CreateExecutionRole(context.Background())
	var setupErr *protocol.BurstSetupError
	if !errors.As(err, &setupErr) {
		t.Errorf("expected BurstSetupError, got %T: %v", err, err)
	}
}

func TestCreateTaskRole_alreadyExists(t *testing.T) {
	const arn = "arn:aws:iam::123:role/burst-task-role"
	c := &IAMClient{client: &mockIAM{
		getRoleFn: func(_ context.Context, _ *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
			return &iam.GetRoleOutput{Role: &types.Role{Arn: aws.String(arn)}}, nil
		},
		createRoleFn: func(_ context.Context, _ *iam.CreateRoleInput, _ ...func(*iam.Options)) (*iam.CreateRoleOutput, error) {
			t.Fatal("CreateRole should not be called")
			return nil, nil
		},
	}, accountID: "123", region: "us-east-1"}

	got, err := c.CreateTaskRole(context.Background(), "burst-us-east-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != arn {
		t.Errorf("got %q, want %q", got, arn)
	}
}
