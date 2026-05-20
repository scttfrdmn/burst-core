package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	internalaws "github.com/scttfrdmn/burst-core/internal/aws"
	"github.com/scttfrdmn/burst-core/internal/config"
)

var (
	checkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	crossStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

func check() string         { return checkStyle.Render("✓") }
func cross() string         { return crossStyle.Render("✗") }
func label(s string) string { return dimStyle.Render(s) }

func newSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Provision AWS infrastructure for burst-core",
		Args:  cobra.NoArgs,
		Run:   runSetup,
	}
}

func runSetup(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	cfg, err := loadAWSConfig(ctx)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("loading AWS config: %v", err))
	}

	// Step 1: validate credentials and get account ID
	stsClient := internalaws.NewSTSClient(cfg)
	identity, err := stsClient.GetCallerIdentity(ctx)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("%s  %s: %v", cross(), label("AWS credentials"), err))
	}

	region := cfg.Region
	if region == "" {
		exitWithCode(2, "region is required — set AWS_REGION, use --region, or configure a default region")
	}
	fmt.Printf("  %s  %s (%s / %s)\n", check(), label("AWS credentials validated"), identity.AccountID, region)

	// Step 2: S3 bucket
	bucketName := rootFlags.bucket
	if bucketName == "" {
		bucketName = "burst-" + region
	}
	s3Client := internalaws.NewS3Client(cfg)
	if err := s3Client.CreateBucket(ctx, bucketName); err != nil {
		exitWithCode(3, fmt.Sprintf("%s  %s: %v", cross(), label("S3 bucket"), err))
	}
	fmt.Printf("  %s  %s: %s\n", check(), label("S3 bucket"), bucketName)

	// Step 3: IAM roles
	iamClient := internalaws.NewIAMClient(cfg, identity.AccountID)
	execRoleARN, err := iamClient.CreateExecutionRole(ctx)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("%s  %s: %v", cross(), label("IAM execution role"), err))
	}
	taskRoleARN, err := iamClient.CreateTaskRole(ctx, bucketName)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("%s  %s: %v", cross(), label("IAM task role"), err))
	}
	fmt.Printf("  %s  %s: burst-execution-role, burst-task-role\n", check(), label("IAM roles"))

	// Step 4: ECS cluster
	ecsClient := internalaws.NewECSClient(cfg)
	if err := ecsClient.CreateCluster(ctx); err != nil {
		exitWithCode(3, fmt.Sprintf("%s  %s: %v", cross(), label("ECS cluster"), err))
	}
	fmt.Printf("  %s  %s: burst-cluster (ACTIVE)\n", check(), label("ECS cluster"))

	// Step 5: Fargate quota
	quotaClient := internalaws.NewQuotaClient(cfg)
	quota, err := quotaClient.GetFargateOnDemandVCPUQuota(ctx)
	if err != nil && err != internalaws.ErrQuotaNotFound {
		exitWithCode(4, fmt.Sprintf("%s  %s: %v", cross(), label("Fargate quota"), err))
	}
	if err == internalaws.ErrQuotaNotFound {
		fmt.Printf("  %s  %s: unknown (quota API unavailable)\n", check(), label("Fargate quota"))
	} else {
		fmt.Printf("  %s  %s: %.0f vCPUs on-demand\n", check(), label("Fargate quota"), quota)
	}

	// Step 6: write config
	ecrBase := fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com", identity.AccountID, region)
	cfg2 := config.DefaultConfig()
	cfg2.Region = region
	cfg2.S3Bucket = bucketName
	cfg2.ECSCluster = "burst-cluster"
	cfg2.ECRBaseURI = ecrBase
	cfg2.ExecutionRoleARN = execRoleARN
	cfg2.TaskRoleARN = taskRoleARN

	if err := cfg2.Save(); err != nil {
		exitWithCode(2, fmt.Sprintf("%s  %s: %v", cross(), label("config"), err))
	}
	cfgPath, _ := config.ConfigPath()
	fmt.Printf("  %s  %s: %s\n", check(), label("Config saved"), cfgPath)

	if rootFlags.json {
		out := map[string]any{
			"region":             region,
			"bucket":             bucketName,
			"account_id":         identity.AccountID,
			"ecr_base_uri":       ecrBase,
			"execution_role_arn": execRoleARN,
			"task_role_arn":      taskRoleARN,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return
	}

	fmt.Printf("\nburst-core setup complete. Run `burst-core status` to verify.\n")
}
