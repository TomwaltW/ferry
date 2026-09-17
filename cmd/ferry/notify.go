package main

import (
	"fmt"
	"strings"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/notify"
	"github.com/spf13/cobra"
)

var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "Work with the wake-up channels",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	},
}

var notifyTestCmd = &cobra.Command{
	Use:       "test <" + strings.Join(config.Channels, "|") + ">",
	Short:     "Send a test reminder through one wake-up channel",
	Args:      cobra.ExactArgs(1),
	ValidArgs: config.Channels,
	RunE: func(cmd *cobra.Command, args []string) error {
		home := homeDir()
		cfg, err := config.Load(config.Path(home))
		if err != nil {
			return err
		}
		channel := args[0]
		// The channel need not be in notify.enabled: testing it first is the
		// whole point of the command.
		if err := notify.Test(cmd.Context(), cfg.Notify, channel); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(),
			defaultBundle().T(cfg.Lang, "cli.notify_ok", map[string]any{"Channel": channel}))
		return nil
	},
}

func init() {
	notifyCmd.AddCommand(notifyTestCmd)
	rootCmd.AddCommand(notifyCmd)
}
