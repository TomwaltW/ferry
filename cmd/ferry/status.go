package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/security"
	"github.com/spf13/cobra"
)

// statusStale is how long a status file may go unrefreshed before the daemon
// is reported as not running; the daemon rewrites it every 5 seconds.
const statusStale = 30 * time.Second

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether the daemon is running and WeChat is online",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		home := homeDir()
		out := cmd.OutOrStdout()

		if _, err := security.LoadCredentials(home, fsp.PlatformWeChat); err != nil {
			fmt.Fprintln(out, "credentials  none (run: ferry login wechat)")
		} else {
			fmt.Fprintln(out, "credentials  stored")
		}

		data, err := os.ReadFile(filepath.Join(home, statusName))
		if err != nil {
			fmt.Fprintln(out, "daemon       not running")
			return nil
		}
		var sf statusFile
		if err := json.Unmarshal(data, &sf); err != nil {
			fmt.Fprintln(out, "daemon       not running (unreadable status file)")
			return nil
		}
		if age := time.Since(sf.WrittenAt); age > statusStale {
			fmt.Fprintf(out, "daemon       not running (status %s old)\n", age.Round(time.Second))
			return nil
		}
		fmt.Fprintln(out, "daemon       running")
		state := "offline"
		if sf.IMOnline {
			state = "online"
		}
		fmt.Fprintf(out, "wechat       %s", state)
		if sf.IMDetail != "" {
			fmt.Fprintf(out, " (%s)", sf.IMDetail)
		}
		fmt.Fprintln(out)
		fmt.Fprintf(out, "bot          %s\n", sf.BotID)
		fmt.Fprintf(out, "sessions     %d total, %d running\n", sf.Sessions, sf.Running)
		if !sf.IMOnline {
			fmt.Fprintln(out, defaultBundle().T(defaultLang(home), "cli.offline", nil))
		}
		return nil
	},
}

func init() { rootCmd.AddCommand(statusCmd) }
