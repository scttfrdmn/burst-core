package aws

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// ErrCEPermissionDenied is returned when ce:GetCostAndUsage permission is missing.
var ErrCEPermissionDenied = errors.New("Cost Explorer access denied: add ce:GetCostAndUsage to your IAM policy")

// ceAPI is a narrow interface over the costexplorer.Client methods used here.
type ceAPI interface {
	GetCostAndUsage(ctx context.Context, in *costexplorer.GetCostAndUsageInput, opts ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error)
}

// CostExplorerClient wraps Cost Explorer operations.
type CostExplorerClient struct {
	client ceAPI
}

// DailyCost holds the USD cost for a single day.
type DailyCost struct {
	Date   string  // "2026-03-15"
	Amount float64 // USD
}

// NewCostExplorerClient creates a CostExplorerClient from an AWS config.
func NewCostExplorerClient(cfg aws.Config) *CostExplorerClient {
	return &CostExplorerClient{client: costexplorer.NewFromConfig(cfg)}
}

// GetBurstCosts returns daily USD costs for burst-core managed resources over the
// past N days, filtered by tag managed-by=burst-core. Returns ErrCEPermissionDenied
// if the caller lacks ce:GetCostAndUsage permission.
// Also returns the sum total.
func (c *CostExplorerClient) GetBurstCosts(ctx context.Context, days int) ([]DailyCost, float64, error) {
	now := time.Now().UTC()
	start := now.AddDate(0, 0, -days).Format("2006-01-02")
	end := now.Format("2006-01-02")

	out, err := c.client.GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
		TimePeriod: &types.DateInterval{
			Start: aws.String(start),
			End:   aws.String(end),
		},
		Granularity: types.GranularityDaily,
		Filter: &types.Expression{
			Tags: &types.TagValues{
				Key:    aws.String("managed-by"),
				Values: []string{"burst-core"},
			},
		},
		Metrics: []string{"UnblendedCost"},
	})
	if err != nil {
		if isCEAccessDenied(err) {
			return nil, 0, ErrCEPermissionDenied
		}
		return nil, 0, fmt.Errorf("querying Cost Explorer: %w", err)
	}

	var daily []DailyCost
	var total float64
	for _, r := range out.ResultsByTime {
		date := ""
		if r.TimePeriod != nil {
			date = aws.ToString(r.TimePeriod.Start)
		}
		amount := 0.0
		if m, ok := r.Total["UnblendedCost"]; ok {
			v, _ := strconv.ParseFloat(aws.ToString(m.Amount), 64)
			amount = v
		}
		daily = append(daily, DailyCost{Date: date, Amount: amount})
		total += amount
	}
	return daily, total, nil
}

// isCEAccessDenied returns true if the error is an access-denied response from Cost Explorer.
func isCEAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "AccessDenied") ||
		strings.Contains(msg, "AuthorizationError") ||
		strings.Contains(msg, "UnauthorizedOperation")
}
