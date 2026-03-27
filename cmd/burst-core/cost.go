package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	internalaws "github.com/scttfrdmn/burst-core/internal/aws"
)

func newCostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cost",
		Short: "Report AWS costs for burst-core resources",
	}
	cmd.AddCommand(newCostReportCmd())
	return cmd
}

func newCostReportCmd() *cobra.Command {
	var days int
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Show daily cost breakdown for burst-core resources",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			runCostReport(cmd.Context(), days)
		},
	}
	cmd.Flags().IntVar(&days, "days", 30, "number of days to include in report")
	return cmd
}

func runCostReport(ctx context.Context, days int) {
	awsCfg, err := loadAWSConfig(ctx)
	if err != nil {
		exitWithCode(3, fmt.Sprintf("loading AWS config: %v", err))
	}

	ceClient := internalaws.NewCostExplorerClient(awsCfg)
	daily, total, err := ceClient.GetBurstCosts(ctx, days)
	if err != nil {
		if errors.Is(err, internalaws.ErrCEPermissionDenied) {
			fmt.Fprintln(os.Stderr, "Cost Explorer requires the ce:GetCostAndUsage IAM permission.")
			fmt.Fprintln(os.Stderr, "Add it to your IAM user or role and retry.")
			os.Exit(2)
		}
		exitWithCode(3, fmt.Sprintf("querying Cost Explorer: %v", err))
	}

	if rootFlags.json {
		out := map[string]any{
			"days":  days,
			"total": total,
			"daily": daily,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return
	}

	fmt.Printf("  %-14s  %s\n", "Date", "Cost (USD)")
	for _, d := range daily {
		fmt.Printf("  %-14s  $%s\n", d.Date, formatDollars(d.Amount))
	}
	fmt.Printf("\n  %-14s  $%s\n", "Total:", formatDollars(total))
}

// formatDollars formats a USD amount with 2 decimal places.
func formatDollars(amount float64) string {
	return strconv.FormatFloat(amount, 'f', 2, 64)
}
