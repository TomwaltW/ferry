// Command ferry bridges IM entry points and coding agents.
package main

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:           "ferry",
	Short:         "Ferry carries your coding agents to chat",
	SilenceErrors: true,
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		rootCmd.PrintErrln("ferry:", err)
		os.Exit(1)
	}
}
