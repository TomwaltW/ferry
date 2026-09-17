package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Injected at release time with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "none"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the ferry version",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "ferry %s (%s)\n", version, commit)
		return err
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
