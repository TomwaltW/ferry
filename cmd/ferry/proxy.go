package main

import (
	"errors"
	"log/slog"

	"github.com/TomwaltW/ferry/internal/driver/proxy"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/spf13/cobra"
)

var (
	proxyAllowInject   bool
	proxyRemoteApprove bool
	proxyTitle         string
)

var proxyCmd = &cobra.Command{
	Use:   "proxy <agent>",
	Short: "Serve an editor as an ACP agent and mirror the session to chat",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		home := homeDir()

		// Stdout carries the protocol, so every word Ferry says of its own
		// accord goes to stderr, where the editor keeps its log.
		log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn}))

		err := proxy.Run(cmd.Context(), proxy.Options{
			Agent:         args[0],
			Title:         proxyTitle,
			AllowInject:   proxyAllowInject,
			RemoteApprove: proxyRemoteApprove,
			Home:          home,
			In:            cmd.InOrStdin(),
			Out:           cmd.OutOrStdout(),
			Err:           cmd.ErrOrStderr(),
			Log:           log,
		})
		if errors.Is(err, fsp.ErrNotFound) {
			// Either there is no configuration yet or the agent is not in it;
			// both are answered by running ferry init.
			cmd.PrintErrln("ferry:", err)
			cmd.PrintErrln(defaultBundle().T(defaultLang(home), "cli.need_init", nil))
			return errSilentExit1
		}
		return err
	},
}

func init() {
	proxyCmd.Flags().BoolVar(&proxyAllowInject, "allow-inject", false, "let chat send prompts while the editor is idle")
	proxyCmd.Flags().BoolVar(&proxyRemoteApprove, "remote-approve", false, "ask chat for permissions instead of the editor")
	proxyCmd.Flags().StringVar(&proxyTitle, "title", "", "name this session goes by in chat")
	rootCmd.AddCommand(proxyCmd)
}
