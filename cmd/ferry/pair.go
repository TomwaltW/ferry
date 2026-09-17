package main

import (
	"fmt"

	"github.com/TomwaltW/ferry/internal/acl"
	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/spf13/cobra"
)

var pairCmd = &cobra.Command{
	Use:   "pair <code>",
	Short: "Confirm a pairing code from the terminal",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		home := homeDir()
		bundle := defaultBundle()
		lang := defaultLang(home)

		cfg, err := config.Load(config.Path(home))
		if err != nil {
			cmd.PrintErrln(bundle.T(lang, "cli.need_init", nil))
			return errSilentExit1
		}
		st, err := openStore(home)
		if err != nil {
			cmd.PrintErrln("ferry:", err)
			return errExit2
		}
		defer st.Close()

		_, sender, err := acl.New(cfg.WeChat, st, clock.Real()).ConfirmPairing(cmd.Context(), args[0])
		if err != nil {
			fmt.Fprintln(cmd.OutOrStdout(), bundle.T(lang, "pair.invalid", nil))
			return errSilentExit1
		}
		fmt.Fprintln(cmd.OutOrStdout(), bundle.T(lang, "pair.confirmed_owner", map[string]any{"Sender": sender}))
		return nil
	},
}

func init() { rootCmd.AddCommand(pairCmd) }
