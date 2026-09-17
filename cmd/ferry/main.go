// Command ferry bridges IM entry points and coding agents.
package main

import (
	"errors"
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
		// errSilentExit1 and errExit2 have already said their piece.
		if !errors.Is(err, errSilentExit1) && !errors.Is(err, errExit2) {
			rootCmd.PrintErrln("ferry:", err)
		}
		os.Exit(exitCode(err))
	}
}
