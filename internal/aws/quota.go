package aws

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas/types"
)

// ErrQuotaNotFound is returned when the Service Quotas API cannot find the
// requested quota. Callers should treat this as "unknown" and proceed with
// a conservative default rather than failing hard.
var ErrQuotaNotFound = errors.New("quota not found in Service Quotas API")

// quotaAPI is a narrow interface over the servicequotas.Client methods used here.
type quotaAPI interface {
	GetServiceQuota(ctx context.Context, in *servicequotas.GetServiceQuotaInput, opts ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error)
}

// QuotaClient wraps Service Quotas lookups used by burst-core.
type QuotaClient struct {
	client quotaAPI
	region string
}

// NewQuotaClient creates a QuotaClient from an AWS config.
func NewQuotaClient(cfg aws.Config) *QuotaClient {
	return &QuotaClient{
		client: servicequotas.NewFromConfig(cfg),
		region: cfg.Region,
	}
}

// Fargate Service Quota codes.
const (
	// QuotaFargateOnDemandVCPU is the quota code for Fargate on-demand vCPU.
	QuotaFargateOnDemandVCPU = "L-3032A538"
	// QuotaFargateSpotVCPU is the quota code for Fargate Spot vCPU.
	QuotaFargateSpotVCPU = "L-7F6USBRE"

	// ecsServiceCode is the AWS service code for ECS/Fargate quotas.
	ecsServiceCode = "fargate"
)

// GetFargateOnDemandVCPUQuota returns the Fargate on-demand vCPU quota value.
// Returns ErrQuotaNotFound if the quota is not available in this region.
func (c *QuotaClient) GetFargateOnDemandVCPUQuota(ctx context.Context) (float64, error) {
	return c.getQuota(ctx, ecsServiceCode, QuotaFargateOnDemandVCPU)
}

// GetFargateSpotVCPUQuota returns the Fargate Spot vCPU quota value.
// Returns ErrQuotaNotFound if the quota is not available in this region.
func (c *QuotaClient) GetFargateSpotVCPUQuota(ctx context.Context) (float64, error) {
	return c.getQuota(ctx, ecsServiceCode, QuotaFargateSpotVCPU)
}

// getQuota fetches a single quota value by service code and quota code.
func (c *QuotaClient) getQuota(ctx context.Context, serviceCode, quotaCode string) (float64, error) {
	out, err := c.client.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{
		ServiceCode: aws.String(serviceCode),
		QuotaCode:   aws.String(quotaCode),
	})
	if err != nil {
		var noSuch *types.NoSuchResourceException
		if errors.As(err, &noSuch) {
			return 0, fmt.Errorf("%w: service=%s quota=%s", ErrQuotaNotFound, serviceCode, quotaCode)
		}
		return 0, fmt.Errorf("getting quota %s/%s: %w", serviceCode, quotaCode, err)
	}

	if out.Quota == nil || out.Quota.Value == nil {
		return 0, fmt.Errorf("%w: service=%s quota=%s (nil value)", ErrQuotaNotFound, serviceCode, quotaCode)
	}

	return aws.ToFloat64(out.Quota.Value), nil
}
