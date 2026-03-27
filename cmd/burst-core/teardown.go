package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	internalaws "github.com/scttfrdmn/burst-core/internal/aws"
	"github.com/scttfrdmn/burst-core/internal/config"
)

func newTeardownCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "teardown",
		Short: "Remove all AWS infrastructure provisioned by burst-core",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if !force {
				exitWithCode(2, "teardown requires --force flag")
			}
			runTeardown(cmd.Context())
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "required: confirm destructive teardown")
	return cmd
}

func runTeardown(ctx context.Context) {
	// Load config to get resource names; fall back to flag-derived names if absent.
	cfg, cfgErr := config.Load()

	awsCfg, err := loadAWSConfig(ctx)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("loading AWS config: %v", err))
	}

	region := awsCfg.Region
	bucketName := rootFlags.bucket
	if bucketName == "" {
		if cfgErr == nil {
			bucketName = cfg.S3Bucket
		} else {
			bucketName = "burst-" + region
		}
	}

	// Get account ID for ECR/IAM clients
	stsClient := internalaws.NewSTSClient(awsCfg)
	identity, err := stsClient.GetCallerIdentity(ctx)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("validating AWS credentials: %v", err))
	}

	var errs []error

	// Step 1: ECR repositories
	ecrClient := internalaws.NewECRClient(awsCfg, identity.AccountID)
	repos, err := ecrClient.ListBurstRepositories(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing ECR repos: %w", err))
		fmt.Printf("  %s  ECR repositories: %v\n", cross(), err)
	} else {
		for _, repo := range repos {
			if err := ecrClient.DeleteRepository(ctx, repo); err != nil {
				errs = append(errs, err)
			}
		}
		fmt.Printf("  %s  ECR repositories deleted (%d repos)\n", check(), len(repos))
	}

	// Step 2: ECS task definitions
	ecsClient := internalaws.NewECSClient(awsCfg)
	families, err := ecsClient.ListTaskDefinitionFamilies(ctx, "burst-")
	if err != nil {
		errs = append(errs, fmt.Errorf("listing task definition families: %w", err))
		fmt.Printf("  %s  ECS task definitions: %v\n", cross(), err)
	} else {
		for _, family := range families {
			if err := ecsClient.DeregisterAllRevisions(ctx, family); err != nil {
				errs = append(errs, err)
			}
		}
		fmt.Printf("  %s  ECS task definitions deregistered (%d families)\n", check(), len(families))
	}

	// Step 3: ECS cluster
	if err := ecsClient.DeleteCluster(ctx); err != nil {
		errs = append(errs, err)
		fmt.Printf("  %s  ECS cluster: %v\n", cross(), err)
	} else {
		fmt.Printf("  %s  ECS cluster deleted: burst-cluster\n", check())
	}

	// Step 4: IAM roles
	iamClient := internalaws.NewIAMClient(awsCfg, identity.AccountID)
	roleErrs := 0
	for _, roleName := range []string{"burst-execution-role", "burst-task-role"} {
		if err := iamClient.DeleteRole(ctx, roleName); err != nil {
			errs = append(errs, err)
			roleErrs++
		}
	}
	if roleErrs > 0 {
		fmt.Printf("  %s  IAM roles: %d deletion error(s)\n", cross(), roleErrs)
	} else {
		fmt.Printf("  %s  IAM roles deleted: burst-execution-role, burst-task-role\n", check())
	}

	// Step 5: S3 bucket
	s3Client := internalaws.NewS3Client(awsCfg)
	if err := s3Client.EmptyAndDeleteBucket(ctx, bucketName); err != nil {
		errs = append(errs, err)
		fmt.Printf("  %s  S3 bucket: %v\n", cross(), err)
	} else {
		fmt.Printf("  %s  S3 bucket deleted: %s\n", check(), bucketName)
	}

	// Step 6: config file
	cfgPath, _ := config.ConfigPath()
	if removeErr := os.Remove(cfgPath); removeErr != nil && !os.IsNotExist(removeErr) {
		errs = append(errs, removeErr)
		fmt.Printf("  %s  Config: %v\n", cross(), removeErr)
	} else {
		fmt.Printf("  %s  Config removed: %s\n", check(), cfgPath)
	}

	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d error(s) during teardown:\n", len(errs))
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  - %v\n", e)
		}
		os.Exit(3)
	}

	fmt.Printf("\nburst-core teardown complete.\n")
}
