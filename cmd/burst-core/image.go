package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	internalaws "github.com/scttfrdmn/burst-core/internal/aws"
	"github.com/scttfrdmn/burst-core/internal/config"
	"github.com/scttfrdmn/burst-core/internal/docker"
)

func newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage burst worker Docker images in ECR",
	}
	cmd.AddCommand(newImageBuildCmd())
	cmd.AddCommand(newImageListCmd())
	cmd.AddCommand(newImagePruneCmd())
	return cmd
}

// ── image build ───────────────────────────────────────────────────────────────

func newImageBuildCmd() *cobra.Command {
	var lang, envHash, dockerfile, buildDir string
	var buildArgs []string

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build and push a burst worker image to ECR",
		Long: `Build a Docker image for burst workers and push it to ECR.

Prints the full ECR image URI to stdout on success (suitable for
capture by language library wrappers).

Exactly one of --dockerfile or --build-dir must be provided:
  --dockerfile  path to a Dockerfile; its parent directory is the build context
  --build-dir   path to a directory containing a Dockerfile and all build files`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImageBuild(cmd.Context(), lang, envHash, dockerfile, buildDir, buildArgs)
		},
	}

	cmd.Flags().StringVar(&lang, "lang", "", "language tag (python, typescript, julia, java, csharp, go, rust) [required]")
	cmd.Flags().StringVar(&envHash, "env-hash", "", "environment hash used as the ECR image tag [required]")
	cmd.Flags().StringVar(&dockerfile, "dockerfile", "", "path to Dockerfile (parent dir is the build context)")
	cmd.Flags().StringVar(&buildDir, "build-dir", "", "path to a directory containing Dockerfile and all build files")
	cmd.Flags().StringArrayVar(&buildArgs, "build-arg", nil, "Docker build argument in KEY=VALUE form (repeatable)")

	_ = cmd.MarkFlagRequired("lang")
	_ = cmd.MarkFlagRequired("env-hash")

	return cmd
}

func runImageBuild(ctx context.Context, lang, envHash, dockerfile, buildDir string, buildArgs []string) error {
	if dockerfile == "" && buildDir == "" {
		return fmt.Errorf("one of --dockerfile or --build-dir is required")
	}
	if dockerfile != "" && buildDir != "" {
		return fmt.Errorf("--dockerfile and --build-dir are mutually exclusive")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	awsCfg, err := loadAWSConfig(ctx)
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}
	stsClient := internalaws.NewSTSClient(awsCfg)
	identity, err := stsClient.GetCallerIdentity(ctx)
	if err != nil {
		return fmt.Errorf("validating AWS credentials: %w", err)
	}
	ecrClient := internalaws.NewECRClient(awsCfg, identity.AccountID)

	opts := docker.BuildOptions{
		Lang:       lang,
		EnvHash:    envHash,
		ECRBaseURI: cfg.ECRBaseURI,
		Region:     cfg.Region,
	}

	// Build arg map
	if len(buildArgs) > 0 {
		opts.BuildArgs = make(map[string]string, len(buildArgs))
		for _, ba := range buildArgs {
			k, v, ok := strings.Cut(ba, "=")
			if !ok {
				return fmt.Errorf("invalid --build-arg %q: expected KEY=VALUE", ba)
			}
			opts.BuildArgs[k] = v
		}
	}

	if buildDir != "" {
		abs, err := filepath.Abs(buildDir)
		if err != nil {
			return fmt.Errorf("resolving --build-dir: %w", err)
		}
		opts.BuildContext = abs
	} else {
		abs, err := filepath.Abs(dockerfile)
		if err != nil {
			return fmt.Errorf("resolving --dockerfile: %w", err)
		}
		// Use the Dockerfile's parent directory as the build context.
		opts.BuildContext = filepath.Dir(abs)
		// If the filename is not the standard "Dockerfile", pass it explicitly
		// so Podman/Docker can find it.
		if base := filepath.Base(abs); base != "Dockerfile" && base != "ContainerFile" {
			opts.DockerfilePath = abs
		}
	}

	// Stream build output to stderr; only the image URI goes to stdout.
	imageURI, err := docker.BuildAndPush(ctx, ecrClient, opts, os.Stderr)
	if err != nil {
		return fmt.Errorf("building image: %w", err)
	}

	fmt.Println(imageURI)
	return nil
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
