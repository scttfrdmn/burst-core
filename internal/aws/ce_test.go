package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

type mockCE struct {
	getCostAndUsageFn func(context.Context, *costexplorer.GetCostAndUsageInput, ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error)
}

func (m *mockCE) GetCostAndUsage(ctx context.Context, in *costexplorer.GetCostAndUsageInput, opts ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error) {
	return m.getCostAndUsageFn(ctx, in, opts...)
}

func TestGetBurstCosts_success(t *testing.T) {
	c := &CostExplorerClient{client: &mockCE{
		getCostAndUsageFn: func(_ context.Context, _ *costexplorer.GetCostAndUsageInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error) {
			return &costexplorer.GetCostAndUsageOutput{
				ResultsByTime: []types.ResultByTime{
					{
						TimePeriod: &types.DateInterval{Start: aws.String("2026-03-25"), End: aws.String("2026-03-26")},
						Total: map[string]types.MetricValue{
							"UnblendedCost": {Amount: aws.String("1.23"), Unit: aws.String("USD")},
						},
					},
					{
						TimePeriod: &types.DateInterval{Start: aws.String("2026-03-26"), End: aws.String("2026-03-27")},
						Total: map[string]types.MetricValue{
							"UnblendedCost": {Amount: aws.String("0.77"), Unit: aws.String("USD")},
						},
					},
				},
			}, nil
		},
	}}

	daily, total, err := c.GetBurstCosts(context.Background(), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(daily) != 2 {
		t.Fatalf("got %d daily entries, want 2", len(daily))
	}
	if daily[0].Date != "2026-03-25" {
		t.Errorf("date: got %q, want 2026-03-25", daily[0].Date)
	}
	const wantTotal = 2.00
	if total < 1.99 || total > 2.01 {
		t.Errorf("total: got %.2f, want %.2f", total, wantTotal)
	}
}

func TestGetBurstCosts_accessDenied(t *testing.T) {
	c := &CostExplorerClient{client: &mockCE{
		getCostAndUsageFn: func(_ context.Context, _ *costexplorer.GetCostAndUsageInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error) {
			return nil, errors.New("AccessDenied: User not authorized to perform ce:GetCostAndUsage")
		},
	}}

	_, _, err := c.GetBurstCosts(context.Background(), 7)
	if !errors.Is(err, ErrCEPermissionDenied) {
		t.Errorf("expected ErrCEPermissionDenied, got %v", err)
	}
}
