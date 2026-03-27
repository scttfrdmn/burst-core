package main

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/spf13/cobra"
)

var rootFlags struct {
	region  string
	profile string
	json    bool
	bucket  string
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:          "burst-core",
		Short:        "AWS cloud bursting — infrastructure management",
		SilenceUsage: true,
	}

	root.PersistentFlags().StringVar(&rootFlags.region, "region", "", "AWS region")
	root.PersistentFlags().StringVar(&rootFlags.profile, "profile", "", "AWS profile")
	root.PersistentFlags().BoolVar(&rootFlags.json, "json", false, "output as JSON")
	root.PersistentFlags().StringVar(&rootFlags.bucket, "bucket", "", "override S3 bucket name")

	root.AddCommand(newSetupCmd())
	root.AddCommand(newTeardownCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newVersionCmd())
	return root
}

// loadAWSConfig builds an AWS config from global flags.
func loadAWSConfig(ctx context.Context) (awssdk.Config, error) {
	opts := []func(*config.LoadOptions) error{}
	if rootFlags.region != "" {
		opts = append(opts, config.WithRegion(rootFlags.region))
	}
	if rootFlags.profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(rootFlags.profile))
	}
	return config.LoadDefaultConfig(ctx, opts...)
}

// exitWithCode prints msg to stderr and exits with code.
func exitWithCode(code int, msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(code)
}
