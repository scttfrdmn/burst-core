package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	internalaws "github.com/scttfrdmn/burst-core/internal/aws"
	"github.com/scttfrdmn/burst-core/internal/config"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Verify provisioned AWS infrastructure",
		Args:  cobra.NoArgs,
		Run:   runStatus,
	}
}

type statusResult struct {
	ConfigFile      string            `json:"config_file"`
	AWSIdentity     string            `json:"aws_identity"`
	S3Bucket        string            `json:"s3_bucket"`
	ECSCluster      string            `json:"ecs_cluster"`
	ExecutionRole   string            `json:"execution_role"`
	TaskRole        string            `json:"task_role"`
	FargateQuotaVCPU float64          `json:"fargate_quota_vcpu"`
	ECRRepositories  map[string]int   `json:"ecr_repositories"`
	ActiveSessions  int               `json:"active_sessions"`
}

func runStatus(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	cfgPath, _ := config.ConfigPath()
	cfg, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			exitWithCode(2, "burst-core is not configured — run: burst-core setup")
		}
		exitWithCode(2, fmt.Sprintf("loading config: %v", err))
	}

	awsCfg, err := loadAWSConfig(ctx)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("loading AWS config: %v", err))
	}

	result := statusResult{
		ConfigFile:      cfgPath,
		ECRRepositories: map[string]int{},
	}

	ok := true
	printRow := func(label, value, status string) {
		fmt.Printf("  %-16s %s %s\n", label+":", value, status)
	}

	// Config file
	printRow("Config file", cfgPath, check())

	// AWS identity
	stsClient := internalaws.NewSTSClient(awsCfg)
	identity, err := stsClient.GetCallerIdentity(ctx)
	if err != nil {
		printRow("AWS identity", "error", cross())
		fmt.Fprintf(os.Stderr, "    %v\n", err)
		ok = false
	} else {
		result.AWSIdentity = identity.ARN
		printRow("AWS identity", identity.ARN, check())
	}

	// S3 bucket
	s3Client := internalaws.NewS3Client(awsCfg)
	bucketExists, err := s3Client.BucketExists(ctx, cfg.S3Bucket)
	if err != nil || !bucketExists {
		sym := cross()
		detail := "not found"
		if err != nil {
			detail = err.Error()
		}
		printRow("S3 bucket", cfg.S3Bucket+" ("+detail+")", sym)
		ok = false
	} else {
		result.S3Bucket = cfg.S3Bucket
		printRow("S3 bucket", cfg.S3Bucket, check())
	}

	// ECS cluster
	ecsClient := internalaws.NewECSClient(awsCfg)
	clusterStatus := ecsStatus(ctx, ecsClient, cfg.ECSCluster)
	if clusterStatus == "" {
		printRow("ECS cluster", cfg.ECSCluster+" (not found)", cross())
		ok = false
	} else {
		result.ECSCluster = cfg.ECSCluster
		printRow("ECS cluster", fmt.Sprintf("%s (%s)", cfg.ECSCluster, clusterStatus), check())
	}

	// IAM roles
	accountID := ""
	if identity != nil {
		accountID = identity.AccountID
	}
	iamClient := internalaws.NewIAMClient(awsCfg, accountID)
	for _, roleName := range []string{"burst-execution-role", "burst-task-role"} {
		exists, err := iamClient.RoleExists(ctx, roleName)
		label := roleName
		if roleName == "burst-execution-role" {
			label = "Execution role"
		} else {
			label = "Task role"
		}
		if err != nil || !exists {
			printRow(label, roleName+" (missing)", cross())
			ok = false
		} else {
			if roleName == "burst-execution-role" {
				result.ExecutionRole = roleName
			} else {
				result.TaskRole = roleName
			}
			printRow(label, roleName, check())
		}
	}

	// Fargate quota
	quotaClient := internalaws.NewQuotaClient(awsCfg)
	quota, err := quotaClient.GetFargateOnDemandVCPUQuota(ctx)
	if err != nil && err != internalaws.ErrQuotaNotFound {
		printRow("Fargate quota", "error", cross())
	} else if err == internalaws.ErrQuotaNotFound {
		printRow("Fargate quota", "unknown", "")
	} else {
		result.FargateQuotaVCPU = quota
		fmt.Printf("  %-16s %.0f vCPUs on-demand\n", "Fargate quota:", quota)
	}

	// ECR repositories
	ecrClient := internalaws.NewECRClient(awsCfg, accountID)
	repos, err := ecrClient.ListBurstRepositories(ctx)
	if err != nil {
		fmt.Printf("\n  ECR repositories: error (%v)\n", err)
	} else {
		fmt.Printf("\n  ECR repositories:\n")
		for _, repo := range repos {
			imageCount := countImages(ctx, ecrClient, repo)
			result.ECRRepositories[repo] = imageCount
			fmt.Printf("    %-30s %d image(s)\n", repo, imageCount)
		}
		if len(repos) == 0 {
			fmt.Printf("    (none)\n")
		}
	}

	// Active sessions (count session directories in S3)
	if result.S3Bucket != "" {
		keys, err := s3Client.ListObjects(ctx, cfg.S3Bucket, "sessions/")
		if err == nil {
			sessionSet := map[string]struct{}{}
			for _, k := range keys {
				// key format: sessions/{session-id}/...
				parts := strings.SplitN(k, "/", 3)
				if len(parts) >= 2 {
					sessionSet[parts[1]] = struct{}{}
				}
			}
			result.ActiveSessions = len(sessionSet)
			fmt.Printf("\n  Active sessions: %d\n", result.ActiveSessions)
		}
	}

	if rootFlags.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}

	if !ok {
		os.Exit(3)
	}
}

// ecsStatus returns the cluster status string, or empty string if not found.
func ecsStatus(ctx context.Context, client *internalaws.ECSClient, clusterName string) string {
	status, err := client.ClusterStatus(ctx)
	if err != nil || status == "" {
		return ""
	}
	return status
}

// countImages returns the number of images in an ECR repository.
func countImages(ctx context.Context, client *internalaws.ECRClient, repoName string) int {
	n, _ := client.ImageCount(ctx, repoName)
	return n
}
