package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/im/feishu"
	"github.com/TomwaltW/ferry/internal/im/wechat"
	"github.com/TomwaltW/ferry/internal/security"
	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
)

// loginPlatforms are the platforms ferry login and ferry logout accept.
var loginPlatforms = []string{string(fsp.PlatformWeChat), string(fsp.PlatformFeishu)}

var loginCmd = &cobra.Command{
	Use:   "login <platform>",
	Short: "Sign in to an IM platform (wechat or feishu)",
	Long: "Sign in to an IM platform.\n\n" +
		"Platforms:\n" +
		"  wechat   scan a QR code with the WeChat account that will own the bot\n" +
		"  feishu   enter the App ID, App Secret and your open_id of a Feishu (Lark) app",
	Example:   "  ferry login wechat\n  ferry login feishu",
	Args:      cobra.ExactArgs(1),
	ValidArgs: loginPlatforms,
	RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case string(fsp.PlatformWeChat):
			return loginWeChat(cmd)
		case string(fsp.PlatformFeishu):
			return loginFeishu(cmd)
		}
		return fmt.Errorf("unsupported platform %q, choose one of %s", args[0], strings.Join(loginPlatforms, ", "))
	},
}

// loginFeishu asks for the app's credentials, checks them against Feishu and
// stores them: the secret in the keyring, the rest in the configuration.
func loginFeishu(cmd *cobra.Command) error {
	home := homeDir()
	if _, err := config.Load(config.Path(home)); err != nil {
		cmd.PrintErrln(defaultBundle().T(defaultLang(home), "cli.need_init", nil))
		return errSilentExit1
	}
	return feishu.LoginInteractive(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), home)
}

func loginWeChat(cmd *cobra.Command) error {
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
}

var logoutCmd = &cobra.Command{
	Use:       "logout <platform>",
	Short:     "Forget the stored credentials of a platform (wechat or feishu)",
	Args:      cobra.ExactArgs(1),
	ValidArgs: loginPlatforms,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !slices.Contains(loginPlatforms, args[0]) {
			return fmt.Errorf("unsupported platform %q, choose one of %s", args[0], strings.Join(loginPlatforms, ", "))
		}
		if err := security.DeleteCredentials(homeDir(), fsp.Platform(args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "removed %s credentials\n", args[0])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
}
