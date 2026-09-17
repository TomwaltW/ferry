package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/registry"
	"github.com/TomwaltW/ferry/internal/security"
	"github.com/spf13/cobra"
)

// doctor reports every check it can make and always exits 0: it is a
// diagnostic, so a missing piece is information rather than a failure.
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check the local setup",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		home := homeDir()
		out := cmd.OutOrStdout()
		report := func(name, state, detail string) {
			if detail == "" {
				fmt.Fprintf(out, "%-22s %s\n", name, state)
				return
			}
			fmt.Fprintf(out, "%-22s %-12s %s\n", name, state, detail)
		}

		report("home", "ok", home)

		cfgPath := config.Path(home)
		cfg, err := config.Load(cfgPath)
		if err != nil {
			report("config", "missing", cfgPath)
			fmt.Fprintln(out, defaultBundle().T(defaultLang(home), "cli.need_init", nil))
			checkTool(out, report)
			return nil
		}
		report("config", "ok", cfgPath)

		if err := cfg.Validate(); err != nil {
			report("config.valid", "invalid", err.Error())
		} else {
			report("config.valid", "ok", "")
		}

		roots, err := cfg.NormalizedRoots()
		if err != nil {
			report("workspace", "invalid", err.Error())
		}
		for _, r := range roots {
			if info, err := os.Stat(r); err != nil || !info.IsDir() {
				report("workspace", "missing", r)
			} else {
				report("workspace", "ok", r)
			}
		}

		if _, err := security.LoadCredentials(home, fsp.PlatformWeChat); err != nil {
			report("credentials", "missing", "run: ferry login wechat")
		} else {
			report("credentials", "ok", "wechat")
		}

		dbPath := filepath.Join(home, dbName)
		if st, err := openStore(home); err != nil {
			report("database", "error", err.Error())
		} else {
			_ = st.Close()
			report("database", "ok", dbPath)
		}

		reg, err := registry.New(cfg.Registry, cfg.Agents, home, slog.New(slog.DiscardHandler))
		if err != nil {
			report("agents", "error", err.Error())
		} else {
			for _, a := range reg.Agents(context.Background()) {
				if a.Available {
					report("agent:"+a.Name, "ok", a.Source)
				} else {
					report("agent:"+a.Name, "unavailable", a.Reason)
				}
			}
		}

		checkTool(out, report)
		return nil
	},
}

// checkTool reports the launchers the registry may need.
func checkTool(_ io.Writer, report func(name, state, detail string)) {
	for _, tool := range []string{"npx", "uvx"} {
		if path, err := exec.LookPath(tool); err != nil {
			report(tool, "missing", "needed for some agent presets")
		} else {
			report(tool, "ok", path)
		}
	}
}

func init() { rootCmd.AddCommand(doctorCmd) }
