package main

import (
	"fmt"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/im/wechat"
	"github.com/TomwaltW/ferry/internal/security"
	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
)

var loginCmd = &cobra.Command{
	Use:   "login <platform>",
	Short: "Sign in to an IM platform",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] != string(fsp.PlatformWeChat) {
			return fmt.Errorf("unsupported platform %q, only wechat is available in this release", args[0])
		}
		home := homeDir()
		bundle := defaultBundle()
		lang := defaultLang(home)

		cfg, err := config.Load(config.Path(home))
		if err != nil {
			cmd.PrintErrln(bundle.T(lang, "cli.need_init", nil))
			return errSilentExit1
		}

		out := cmd.OutOrStdout()
		login := wechat.NewLogin(cfg.WeChat, clock.Real())
		qr, err := login.Start(cmd.Context())
		if err != nil {
			return err
		}
		show := func(content string) {
			fmt.Fprintln(out, bundle.T(lang, "cli.scan_qr", nil))
			qrterminal.GenerateHalfBlock(content, qrterminal.L, out)
		}
		show(qr)

		creds, err := login.Wait(cmd.Context(), show)
		if err != nil {
			return err
		}
		if err := security.SaveCredentials(home, fsp.PlatformWeChat, creds); err != nil {
			return err
		}
		fmt.Fprintln(out, bundle.T(lang, "cli.login_ok", map[string]any{"Bot": creds.BotID, "Owner": creds.OwnerID}))
		return nil
	},
}

var logoutCmd = &cobra.Command{
	Use:   "logout <platform>",
	Short: "Forget the stored credentials of a platform",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] != string(fsp.PlatformWeChat) {
			return fmt.Errorf("unsupported platform %q, only wechat is available in this release", args[0])
		}
		if err := security.DeleteCredentials(homeDir(), fsp.PlatformWeChat); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "removed wechat credentials")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
}
