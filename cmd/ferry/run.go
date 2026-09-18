package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/TomwaltW/ferry/internal/driver/pty"
)

var (
	runName        string
	runIdleMS      int
	runPromptRegex string
)

var runCmd = &cobra.Command{
	Use:   "run [--name <name>] [--idle <ms>] [--prompt-regex <re>] -- <command> [args...]",
	Short: "Wrap a terminal program so chat can watch it and type into it",
	Long: "Run a program that has no protocol of its own inside a pseudo terminal.\n" +
		"The terminal stays yours; the daemon mirrors the output to chat and types\n" +
		"what chat sends back. Without a daemon it is a plain wrapper.",
	Args:                  cobra.MinimumNArgs(1),
	SilenceUsage:          true,
	DisableFlagsInUseLine: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		home := homeDir()

		if !pty.Supported() {
			fmt.Fprintln(cmd.ErrOrStderr(), defaultBundle().T(defaultLang(home), "cli.run_unsupported", nil))
			return errSilentExit1
		}

		code, err := pty.Run(cmd.Context(), pty.Options{
			Name:        runName,
			Idle:        time.Duration(runIdleMS) * time.Millisecond,
			PromptRegex: runPromptRegex,
			Command:     args,
			Home:        home,
			In:          os.Stdin,
			Out:         os.Stdout,
			// No logger: the terminal belongs to the wrapped program, and a log
			// line in the middle of it would corrupt what the user sees.
			Log: nil,
		})
		if err != nil {
			return err
		}

		// The wrapped program's exit code is the one ferry run exits with, and
		// cobra's error path can only say 1 or 2 — so this is the one command
		// that leaves by itself.
		if code != 0 {
			os.Exit(code)
		}
		return nil
	},
}

func init() {
	runCmd.Flags().StringVar(&runName, "name", "", "what to call this session in chat; the command by default")
	runCmd.Flags().IntVar(&runIdleMS, "idle", int(pty.DefaultIdle/time.Millisecond),
		"milliseconds of quiet before a message from chat is typed in")
	runCmd.Flags().StringVar(&runPromptRegex, "prompt-regex", "",
		"only type into the program when its last line matches this")
	rootCmd.AddCommand(runCmd)
}
