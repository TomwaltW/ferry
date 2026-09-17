package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/registry"
	"github.com/spf13/cobra"
)

var initFlags struct {
	lang  string
	roots []string
	agent string
	yes   bool
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create the Ferry configuration",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		home := homeDir()
		path := config.Path(home)
		cfg := config.Default()

		in := bufio.NewReader(cmd.InOrStdin())
		out := cmd.OutOrStdout()
		ask := func(prompt, def string) string {
			if initFlags.yes {
				return def
			}
			fmt.Fprintf(out, "%s [%s]: ", prompt, def)
			line, err := in.ReadString('\n')
			if err != nil && line == "" {
				return def
			}
			if v := strings.TrimSpace(line); v != "" {
				return v
			}
			return def
		}

		// language
		lang := initFlags.lang
		if lang == "" {
			lang = ask("language (zh-CN / en)", string(cfg.Lang))
		}
		cfg.Lang = fsp.Lang(lang)

		// workspace roots
		roots := initFlags.roots
		if len(roots) == 0 {
			def := filepath.Join("~", "code")
			roots = splitRoots(ask("workspace roots (comma separated)", def))
		}
		cfg.Workspace.Roots = roots

		// default agent
		agent := initFlags.agent
		if agent == "" {
			agent = ask(fmt.Sprintf("default agent (%s)", strings.Join(agentNames(cfg), " / ")), cfg.DefaultAgent)
		}
		cfg.DefaultAgent = agent

		if err := cfg.Validate(); err != nil {
			return err
		}
		if err := os.MkdirAll(home, 0o700); err != nil {
			return err
		}
		if err := config.Save(path, cfg); err != nil {
			return err
		}
		fmt.Fprintf(out, "wrote %s\n", path)
		printAgentAvailability(out, home, cfg)
		return nil
	},
}

func splitRoots(s string) []string {
	var roots []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			roots = append(roots, p)
		}
	}
	return roots
}

func agentNames(cfg config.Config) []string {
	names := make([]string, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		names = append(names, a.Name)
	}
	return names
}

// printAgentAvailability lists which of the configured agents can be launched
// right now, so `ferry init` ends with something actionable.
func printAgentAvailability(out io.Writer, home string, cfg config.Config) {
	reg, err := registry.New(cfg.Registry, cfg.Agents, home, slog.New(slog.DiscardHandler))
	if err != nil {
		return
	}
	for _, a := range reg.Agents(context.Background()) {
		status := "available"
		if !a.Available {
			status = "unavailable: " + a.Reason
		}
		fmt.Fprintf(out, "  agent %-16s %s\n", a.Name, status)
	}
}

func init() {
	initCmd.Flags().StringVar(&initFlags.lang, "lang", "", "bot language, zh-CN or en")
	initCmd.Flags().StringArrayVar(&initFlags.roots, "root", nil, "workspace root (repeatable)")
	initCmd.Flags().StringVar(&initFlags.agent, "agent", "", "default agent")
	initCmd.Flags().BoolVar(&initFlags.yes, "yes", false, "accept defaults, ask nothing")
	rootCmd.AddCommand(initCmd)
}
