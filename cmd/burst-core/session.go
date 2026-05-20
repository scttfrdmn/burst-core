package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	internalaws "github.com/scttfrdmn/burst-core/internal/aws"
	"github.com/scttfrdmn/burst-core/internal/config"
	"github.com/scttfrdmn/burst-core/internal/session"
	"github.com/scttfrdmn/burst-core/pkg/protocol"
)

func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage burst sessions",
	}
	cmd.AddCommand(newSessionListCmd())
	cmd.AddCommand(newSessionStatusCmd())
	cmd.AddCommand(newSessionCleanupCmd())
	return cmd
}

// ── session list ──────────────────────────────────────────────────────────────

func newSessionListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all burst sessions",
		Args:  cobra.NoArgs,
		Run:   runSessionList,
	}
}

func runSessionList(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	cfg := mustLoadConfig()
	s3c := mustS3Client(ctx, cfg)

	sessions, err := session.ListSessions(ctx, s3c, cfg.S3Bucket)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("listing sessions: %v", err))
	}

	if rootFlags.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(sessions)
		return
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return
	}

	fmt.Printf("%-26s %-4s %-12s %-10s %-10s %s\n",
		"SESSION ID", "LANG", "STATUS", "TASKS", "COST", "AGE")
	for _, m := range sessions {
		tasks := fmt.Sprintf("%d/%d", m.TasksComplete, m.TasksTotal)
		cost := fmt.Sprintf("$%.2f", m.CostActual)
		age := humanAge(m.CreatedAt)
		fmt.Printf("%-26s %-4s %-12s %-10s %-10s %s\n",
			m.SessionID, m.Language, m.Status, tasks, cost, age)
	}
}

// ── session status ────────────────────────────────────────────────────────────

func newSessionStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <session-id>",
		Short: "Show status of a session",
		Args:  cobra.ExactArgs(1),
		Run:   runSessionStatus,
	}
}

func runSessionStatus(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	cfg := mustLoadConfig()
	s3c := mustS3Client(ctx, cfg)

	m, err := session.ReadManifest(ctx, s3c, cfg.S3Bucket, args[0])
	if err != nil {
		exitWithCode(3, fmt.Sprintf("reading session %q: %v", args[0], err))
	}

	if rootFlags.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(m)
		return
	}

	printManifest(m)
}

func printManifest(m *protocol.Manifest) {
	fmt.Printf("  %-20s %s\n", "Session ID:", m.SessionID)
	fmt.Printf("  %-20s %s\n", "Language:", m.Language)
	fmt.Printf("  %-20s %s\n", "Status:", m.Status)
	fmt.Printf("  %-20s %d/%d\n", "Tasks:", m.TasksComplete, m.TasksTotal)
	if m.TasksFailed > 0 {
		fmt.Printf("  %-20s %d\n", "Failed:", m.TasksFailed)
	}
	fmt.Printf("  %-20s %d\n", "Workers:", m.WorkersActual)
	fmt.Printf("  %-20s %d vCPU / %d GB\n", "Resources:", m.CPU, m.MemoryGB)
	fmt.Printf("  %-20s %s\n", "Created:", m.CreatedAt.Format(time.RFC3339))
	fmt.Printf("  %-20s %.1fs\n", "Elapsed:", m.ElapsedSeconds)
	fmt.Printf("  %-20s $%.4f/hr (actual: $%.4f)\n", "Cost:", m.CostEstimate, m.CostActual)
	fmt.Printf("  %-20s %s\n", "Region:", m.Region)
	fmt.Printf("  %-20s %s\n", "Backend:", m.Backend)
}

// ── session cleanup ───────────────────────────────────────────────────────────

func newSessionCleanupCmd() *cobra.Command {
	var (
		force     bool
		all       bool
		olderThan string
	)
	cmd := &cobra.Command{
		Use:   "cleanup [session-id]",
		Short: "Delete session artifacts from S3",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			runSessionCleanup(cmd.Context(), args, force, all, olderThan)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "delete even if session is still running")
	cmd.Flags().BoolVar(&all, "all", false, "clean up all sessions (use with --older-than)")
	cmd.Flags().StringVar(&olderThan, "older-than", "", "only clean up sessions older than this duration (e.g. 7d, 24h)")
	return cmd
}

func runSessionCleanup(ctx context.Context, args []string, force, all bool, olderThan string) {
	cfg := mustLoadConfig()
	s3c := mustS3Client(ctx, cfg)

	if all {
		runSessionCleanupAll(ctx, s3c, cfg.S3Bucket, force, olderThan)
		return
	}
	if len(args) == 0 {
		exitWithCode(2, "session-id required (or use --all)")
	}
	sessionID := args[0]

	if !force {
		m, err := session.ReadManifest(ctx, s3c, cfg.S3Bucket, sessionID)
		if err == nil && m.Status == "running" {
			exitWithCode(2, fmt.Sprintf("session %q is still running; use --force to delete anyway", sessionID))
		}
	}

	if err := session.DeleteSession(ctx, s3c, cfg.S3Bucket, sessionID); err != nil {
		exitWithCode(3, fmt.Sprintf("cleanup %q: %v", sessionID, err))
	}
	fmt.Printf("  %s  Session %s deleted\n", check(), sessionID)
}

func runSessionCleanupAll(ctx context.Context, s3c *internalaws.S3Client, bucket string, force bool, olderThan string) {
	var minAge time.Duration
	if olderThan != "" {
		d, err := parseDuration(olderThan)
		if err != nil {
			exitWithCode(2, fmt.Sprintf("invalid --older-than value %q: %v", olderThan, err))
		}
		minAge = d
	}

	sessions, err := session.ListSessions(ctx, s3c, bucket)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("listing sessions: %v", err))
	}

	deleted := 0
	for _, m := range sessions {
		if minAge > 0 && time.Since(m.CreatedAt) < minAge {
			continue
		}
		if !force && m.Status == "running" {
			fmt.Printf("  %s  Skipping running session %s (use --force)\n", cross(), m.SessionID)
			continue
		}
		if err := session.DeleteSession(ctx, s3c, bucket, m.SessionID); err != nil {
			fmt.Fprintf(os.Stderr, "  %s  %s: %v\n", cross(), m.SessionID, err)
			continue
		}
		deleted++
		fmt.Printf("  %s  %s\n", check(), m.SessionID)
	}
	fmt.Printf("\nDeleted %d session(s).\n", deleted)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func mustLoadConfig() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			exitWithCode(2, "burst-core is not configured — run: burst-core setup")
		}
		exitWithCode(2, fmt.Sprintf("loading config: %v", err))
	}
	return cfg
}

func mustS3Client(ctx context.Context, cfg *config.Config) *internalaws.S3Client {
	awsCfg, err := loadAWSConfig(ctx)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("loading AWS config: %v", err))
	}
	_ = cfg
	return internalaws.NewS3Client(awsCfg)
}

// parseDuration parses a duration string like "7d", "30d", "24h", "1h30m".
// Handles the "d" (days) suffix not supported by time.ParseDuration.
func parseDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, fmt.Errorf("invalid days value: %w", err)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// humanAge returns a human-readable age string for a given time.
func humanAge(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}
