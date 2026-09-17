package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/TomwaltW/ferry/internal/config"
	"github.com/TomwaltW/ferry/internal/control"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/handoff"
	"github.com/spf13/cobra"
)

// backUnsupportedExit is the exit code spec-p2 3.4 reserves for an agent Ferry
// cannot resume in a terminal. It bypasses exitCode, which only knows 1 and 2.
const backUnsupportedExit = 3

// errNeedInit keeps a missing configuration apart from a missing session: both
// are fsp.ErrNotFound underneath, and they read very differently to a user.
var errNeedInit = errors.New("ferry: not configured")

var (
	backSlot int
	backExec bool
)

var backCmd = &cobra.Command{
	Use:   "back",
	Short: "Park a session and print the command that resumes it in a terminal",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		home := homeDir()
		bundle := defaultBundle()
		lang := defaultLang(home)
		out := cmd.OutOrStdout()

		resp, daemonUp, err := backTarget(cmd.Context(), home, backSlot)
		if errors.Is(err, errNeedInit) {
			cmd.PrintErrln(bundle.T(lang, "cli.need_init", nil))
			return errSilentExit1
		}
		if errors.Is(err, fsp.ErrNotFound) {
			fmt.Fprintln(out, bundle.T(lang, "back.no_session", nil))
			return errSilentExit1
		}
		if err != nil {
			return err
		}
		if !daemonUp {
			// Nothing was parked, because nothing is running: the command comes
			// from the database alone.
			fmt.Fprintln(out, bundle.T(lang, "back.daemon_down", nil))
		}
		if len(resp.BackCommand) == 0 {
			fmt.Fprintln(out, bundle.T(lang, "back.unsupported", map[string]any{
				"Agent": resp.Agent,
				"ID":    resp.AgentSessionID,
			}))
			os.Exit(backUnsupportedExit)
			return nil
		}

		fmt.Fprintln(out, bundle.T(lang, "back.command", map[string]any{"Command": handoff.ShellJoin(resp.BackCommand)}))
		if backExec {
			return handoff.Exec(resp.Cwd, resp.BackCommand)
		}
		return nil
	},
}

// backTarget asks the daemon to hand the session back. Without a daemon there is
// nothing to park, so the command is built from the database instead; the second
// result says which of the two happened.
func backTarget(ctx context.Context, home string, slot int) (control.ParkResponse, bool, error) {
	client, err := control.Dial(home)
	if err == nil {
		defer client.Close()
		resp, err := client.Park(ctx, control.ParkRequest{Slot: slot})
		return resp, true, err
	}
	if !errors.Is(err, fsp.ErrNotFound) {
		return control.ParkResponse{}, false, err
	}
	resp, err := backFromStore(ctx, home, slot)
	return resp, false, err
}

func backFromStore(ctx context.Context, home string, slot int) (control.ParkResponse, error) {
	cfg, err := config.Load(config.Path(home))
	if err != nil {
		if errors.Is(err, fsp.ErrNotFound) {
			return control.ParkResponse{}, errNeedInit
		}
		return control.ParkResponse{}, err
	}
	st, err := openStore(home)
	if err != nil {
		return control.ParkResponse{}, err
	}
	defer st.Close()

	ho := handoff.New(handoff.Options{Agents: cfg.Agents, Profiles: cfg.Handoff.Profiles, Store: st})
	rec, cmd, err := handoff.Local(ctx, st, ho, slot)
	if err != nil {
		return control.ParkResponse{}, err
	}
	return control.ParkResponse{
		Agent:          rec.Agent,
		AgentSessionID: rec.AgentSessionID,
		Cwd:            rec.Cwd,
		BackCommand:    cmd,
	}, nil
}

func init() {
	backCmd.Flags().IntVar(&backSlot, "slot", 0, "session number to hand back; the foreground session by default")
	backCmd.Flags().BoolVar(&backExec, "exec", false, "run the resume command instead of printing it")
	rootCmd.AddCommand(backCmd)
}
