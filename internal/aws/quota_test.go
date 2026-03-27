package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas/types"
)

type mockQuota struct {
	getServiceQuotaFn func(context.Context, *servicequotas.GetServiceQuotaInput, ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error)
}

func (m *mockQuota) GetServiceQuota(ctx context.Context, in *servicequotas.GetServiceQuotaInput, opts ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error) {
	return m.getServiceQuotaFn(ctx, in, opts...)
}

func TestGetFargateOnDemandVCPUQuota(t *testing.T) {
	c := &QuotaClient{client: &mockQuota{
		getServiceQuotaFn: func(_ context.Context, in *servicequotas.GetServiceQuotaInput, _ ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error) {
			if aws.ToString(in.QuotaCode) != QuotaFargateOnDemandVCPU {
				t.Errorf("wrong quota code: %q", aws.ToString(in.QuotaCode))
			}
			return &servicequotas.GetServiceQuotaOutput{
				Quota: &types.ServiceQuota{Value: aws.Float64(256.0)},
			}, nil
		},
	}}

	val, err := c.GetFargateOnDemandVCPUQuota(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 256.0 {
		t.Errorf("got %.1f, want 256.0", val)
	}
}

func TestGetFargateSpotVCPUQuota(t *testing.T) {
	c := &QuotaClient{client: &mockQuota{
		getServiceQuotaFn: func(_ context.Context, in *servicequotas.GetServiceQuotaInput, _ ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error) {
			if aws.ToString(in.QuotaCode) != QuotaFargateSpotVCPU {
				t.Errorf("wrong quota code: %q", aws.ToString(in.QuotaCode))
			}
			return &servicequotas.GetServiceQuotaOutput{
				Quota: &types.ServiceQuota{Value: aws.Float64(128.0)},
			}, nil
		},
	}}

	val, err := c.GetFargateSpotVCPUQuota(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 128.0 {
		t.Errorf("got %.1f, want 128.0", val)
	}
}

func TestGetQuota_notFound(t *testing.T) {
	c := &QuotaClient{client: &mockQuota{
		getServiceQuotaFn: func(_ context.Context, _ *servicequotas.GetServiceQuotaInput, _ ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error) {
			return nil, &types.NoSuchResourceException{}
		},
	}}

	_, err := c.GetFargateOnDemandVCPUQuota(context.Background())
	if !errors.Is(err, ErrQuotaNotFound) {
		t.Errorf("expected ErrQuotaNotFound, got %v", err)
	}
}
