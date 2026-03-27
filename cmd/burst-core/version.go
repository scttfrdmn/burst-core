package main

import (
	"fmt"

	"github.com/scttfrdmn/burst-core/cmd/burst-core/internal/version"
	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print burst-core version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("burst-core %s\n", version.Version)
		},
	}
}
