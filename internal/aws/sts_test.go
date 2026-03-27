package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

type mockSTS struct {
	getCallerIdentityFn func(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

func (m *mockSTS) GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, opts ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return m.getCallerIdentityFn(ctx, in, opts...)
}

func TestGetCallerIdentity_success(t *testing.T) {
	c := &STSClient{client: &mockSTS{
		getCallerIdentityFn: func(_ context.Context, _ *sts.GetCallerIdentityInput, _ ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
			return &sts.GetCallerIdentityOutput{
				Account: aws.String("123456789012"),
				Arn:     aws.String("arn:aws:iam::123456789012:user/scott"),
				UserId:  aws.String("AIDAXXXXXXXXXXXXXXXX"),
			}, nil
		},
	}}

	id, err := c.GetCallerIdentity(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.AccountID != "123456789012" {
		t.Errorf("AccountID: got %q, want %q", id.AccountID, "123456789012")
	}
	if id.ARN != "arn:aws:iam::123456789012:user/scott" {
		t.Errorf("ARN: got %q", id.ARN)
	}
}

func TestGetCallerIdentity_failure(t *testing.T) {
	c := &STSClient{client: &mockSTS{
		getCallerIdentityFn: func(_ context.Context, _ *sts.GetCallerIdentityInput, _ ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
			return nil, errors.New("NoCredentialProviders: no valid providers")
		},
	}}

	_, err := c.GetCallerIdentity(context.Background())
	var setupErr *protocol.BurstSetupError
	if !errors.As(err, &setupErr) {
		t.Errorf("expected BurstSetupError, got %T: %v", err, err)
	}
	if setupErr.Remediation != "run: aws configure" {
		t.Errorf("Remediation: got %q", setupErr.Remediation)
	}
}
