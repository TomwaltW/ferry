package main

import (
	"github.com/TomwaltW/ferry/internal/audit"
	"github.com/TomwaltW/ferry/internal/clock"
	"github.com/TomwaltW/ferry/internal/ports"
	"github.com/spf13/cobra"
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Read the audit log",
	Args:  cobra.NoArgs,
}

var auditFlags struct {
	since  string
	format string
}

var auditExportCmd = &cobra.Command{
	Use:          "export",
	Short:        "Write the audit log to stdout as CSV",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		home := homeDir()
		bundle := defaultBundle()
		lang := defaultLang(home)

		if auditFlags.format != "csv" {
			cmd.PrintErrln(bundle.T(lang, "cli.audit_format_unsupported", map[string]any{"Format": auditFlags.format}))
			return errExit2
		}
		window, err := audit.ParseSince(auditFlags.since)
		if err != nil {
			cmd.PrintErrln(bundle.T(lang, "cli.audit_since_invalid", map[string]any{"Value": auditFlags.since}))
			return errExit2
		}

		st, err := openStore(home)
		if err != nil {
			cmd.PrintErrln("ferry:", err)
			return errExit2
		}
		defer st.Close()
		reader, ok := st.(ports.AuditReader)
		if !ok {
			cmd.PrintErrf("ferry: %T cannot read the audit log\n", st)
			return errExit2
		}

		entries, err := reader.AuditSince(cmd.Context(), clock.Real().Now().Add(-window))
		if err != nil {
			cmd.PrintErrln("ferry:", err)
			return errExit2
		}
		return audit.WriteCSV(cmd.OutOrStdout(), entries)
	},
}

func init() {
	auditExportCmd.Flags().StringVar(&auditFlags.since, "since", "7d", "how far back to export: <n>d, or a duration such as 12h or 30m")
	auditExportCmd.Flags().StringVar(&auditFlags.format, "format", "csv", "output format; only csv is supported")
	auditCmd.AddCommand(auditExportCmd)
	rootCmd.AddCommand(auditCmd)
}
