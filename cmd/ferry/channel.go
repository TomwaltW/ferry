package main

import (
	"log/slog"
	"os"

	"github.com/TomwaltW/ferry/internal/driver/claudechannel"
	"github.com/spf13/cobra"
)

var channelCmd = &cobra.Command{
	Use:   "channel",
	Short: "Serve this session as a Claude Code channel (a stdio MCP server)",
	Long: "Claude Code starts this command; it is not run by hand. Messages from IM " +
		"reach the model through it, the model's replies go back the same way, and " +
		"tool approvals are relayed to whoever owns the session.\n\n" +
		"Stdin and stdout carry the protocol, so everything the command has to say " +
		"goes to stderr. Without a running daemon it still completes the handshake, " +
		"but relays nothing.",
	Args: cobra.NoArgs,
	// Stdout belongs to the protocol: cobra must not print usage into it.
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return claudechannel.Run(cmd.Context(), claudechannel.Options{
			Home: homeDir(),
			In:   cmd.InOrStdin(),
			Out:  os.Stdout,
			Log:  slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil)),
		})
	},
}

func init() { rootCmd.AddCommand(channelCmd) }
