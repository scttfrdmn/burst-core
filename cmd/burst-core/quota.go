package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	internalaws "github.com/scttfrdmn/burst-core/internal/aws"
)

func newQuotaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quota",
		Short: "Check AWS service quotas for burst-core",
	}
	cmd.AddCommand(newQuotaCheckCmd())
	return cmd
}

func newQuotaCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Check Fargate vCPU quotas",
		Args:  cobra.NoArgs,
		Run:   runQuotaCheck,
	}
}

type quotaResult struct {
	OnDemandVCPU float64 `json:"fargate_on_demand_vcpu"`
	SpotVCPU     float64 `json:"fargate_spot_vcpu"`
	Region       string  `json:"region"`
}

func runQuotaCheck(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	awsCfg, err := loadAWSConfig(ctx)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("loading AWS config: %v", err))
	}

	region := awsCfg.Region
	quotaClient := internalaws.NewQuotaClient(awsCfg)

	onDemand, errOD := quotaClient.GetFargateOnDemandVCPUQuota(ctx)
	spot, errSpot := quotaClient.GetFargateSpotVCPUQuota(ctx)

	if rootFlags.json {
		result := quotaResult{Region: region}
		if errOD == nil {
			result.OnDemandVCPU = onDemand
		}
		if errSpot == nil {
			result.SpotVCPU = spot
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}

	consoleLink := fmt.Sprintf(
		"https://console.aws.amazon.com/servicequotas/home?region=%s#!/services/fargate/quotas",
		region,
	)

	if errOD == nil {
		fmt.Printf("  %-28s %.1f vCPUs\n", "Fargate on-demand vCPUs:", onDemand)
	} else if errOD == internalaws.ErrQuotaNotFound {
		fmt.Printf("  %-28s (quota not available in this region)\n", "Fargate on-demand vCPUs:")
	} else {
		fmt.Printf("  %-28s error: %v\n", "Fargate on-demand vCPUs:", errOD)
	}

	if errSpot == nil {
		fmt.Printf("  %-28s %.1f vCPUs\n", "Fargate Spot vCPUs:", spot)
	} else if errSpot == internalaws.ErrQuotaNotFound {
		fmt.Printf("  %-28s (quota not available in this region)\n", "Fargate Spot vCPUs:")
	} else {
		fmt.Printf("  %-28s error: %v\n", "Fargate Spot vCPUs:", errSpot)
	}

	fmt.Printf("\n  Request increase: %s\n", consoleLink)
}
