package main

import (
	"fmt"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/spf13/cobra"
)

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List every session in the database",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		home := homeDir()
		bundle := defaultBundle()
		lang := defaultLang(home)

		st, err := openStore(home)
		if err != nil {
			cmd.PrintErrln("ferry:", err)
			return errExit2
		}
		defer st.Close()

		sessions, err := st.ActiveSessions(cmd.Context())
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(sessions) == 0 {
			fmt.Fprintln(out, bundle.T(lang, "hint.no_session", nil))
			return nil
		}
		fmt.Fprintln(out, bundle.T(lang, "session.list_header", nil))
		for i, s := range sessions {
			fmt.Fprintln(out, bundle.T(lang, "session.list_item", map[string]any{
				"Mark":  " ",
				"Slot":  i + 1,
				"Agent": s.Agent,
				"Cwd":   s.Cwd,
				"State": bundle.T(lang, stateKey(s.State), nil),
			}))
		}
		return nil
	},
}

func stateKey(s fsp.SessionState) string { return "session.state." + string(s) }

func init() { rootCmd.AddCommand(lsCmd) }
