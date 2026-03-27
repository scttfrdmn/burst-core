package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	internalaws "github.com/scttfrdmn/burst-core/internal/aws"
)

func newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage burst worker Docker images in ECR",
	}
	cmd.AddCommand(newImageListCmd())
	cmd.AddCommand(newImagePruneCmd())
	return cmd
}

// ── image list ────────────────────────────────────────────────────────────────

func newImageListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List burst worker images in ECR",
		Args:  cobra.NoArgs,
		Run:   runImageList,
	}
}

type imageRow struct {
	Repository string    `json:"repository"`
	Tag        string    `json:"tag"`
	SizeMB     float64   `json:"size_mb"`
	PushedAt   time.Time `json:"pushed_at"`
}

func runImageList(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	cfg := mustLoadConfig()
	awsCfg, _ := loadAWSConfig(ctx)

	stsClient := internalaws.NewSTSClient(awsCfg)
	identity, err := stsClient.GetCallerIdentity(ctx)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("validating AWS credentials: %v", err))
	}

	ecrClient := internalaws.NewECRClient(awsCfg, identity.AccountID)
	_ = cfg

	repos, err := ecrClient.ListBurstRepositories(ctx)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("listing ECR repositories: %v", err))
	}

	var rows []imageRow
	for _, repo := range repos {
		images, err := ecrClient.ListImages(ctx, repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: listing images in %q: %v\n", repo, err)
			continue
		}
		for _, img := range images {
			tag := img.Digest
			if len(img.Tags) > 0 {
				tag = img.Tags[0]
			}
			rows = append(rows, imageRow{
				Repository: repo,
				Tag:        tag,
				SizeMB:     float64(img.SizeBytes) / 1_000_000,
				PushedAt:   img.PushedAt,
			})
		}
	}

	if rootFlags.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return
	}

	if len(rows) == 0 {
		fmt.Println("No images found.")
		return
	}

	fmt.Printf("%-30s %-44s %8s  %s\n", "REPOSITORY", "TAG", "SIZE", "PUSHED")
	for _, r := range rows {
		tag := r.Tag
		if len(tag) > 42 {
			tag = tag[:42] + ".."
		}
		pushed := r.PushedAt.Format("2006-01-02")
		fmt.Printf("%-30s %-44s %6.1fMB  %s\n", r.Repository, tag, r.SizeMB, pushed)
	}
}

// ── image prune ───────────────────────────────────────────────────────────────

func newImagePruneCmd() *cobra.Command {
	var olderThan string
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete old burst worker images from ECR",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			runImagePrune(cmd.Context(), olderThan)
		},
	}
	cmd.Flags().StringVar(&olderThan, "older-than", "30d", "delete images older than this duration (e.g. 30d, 7d)")
	return cmd
}

func runImagePrune(ctx context.Context, olderThan string) {
	minAge, err := parseDuration(olderThan)
	if err != nil {
		exitWithCode(2, fmt.Sprintf("invalid --older-than value %q: %v", olderThan, err))
	}

	awsCfg, _ := loadAWSConfig(ctx)
	stsClient := internalaws.NewSTSClient(awsCfg)
	identity, err := stsClient.GetCallerIdentity(ctx)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("validating AWS credentials: %v", err))
	}

	ecrClient := internalaws.NewECRClient(awsCfg, identity.AccountID)
	repos, err := ecrClient.ListBurstRepositories(ctx)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("listing ECR repositories: %v", err))
	}

	totalDeleted := 0
	for _, repo := range repos {
		images, err := ecrClient.ListImages(ctx, repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s  %s: %v\n", cross(), repo, err)
			continue
		}
		var toDelete []string
		for _, img := range images {
			if !img.PushedAt.IsZero() && time.Since(img.PushedAt) > minAge {
				toDelete = append(toDelete, img.Digest)
			}
		}
		if len(toDelete) == 0 {
			continue
		}
		if err := ecrClient.DeleteImages(ctx, repo, toDelete); err != nil {
			fmt.Fprintf(os.Stderr, "  %s  %s: %v\n", cross(), repo, err)
			continue
		}
		totalDeleted += len(toDelete)
		fmt.Printf("  %s  %s: deleted %d image(s)\n", check(), repo, len(toDelete))
	}
	fmt.Printf("\nPruned %d image(s) older than %s.\n", totalDeleted, olderThan)
}
