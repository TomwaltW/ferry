package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"

	kservice "github.com/kardianos/service"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/TomwaltW/ferry/internal/service"
)

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Run Ferry as a service of this system, as you",
	Args:  cobra.NoArgs,
}

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the Ferry service and start it",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return serviceDo(cmd, service.Install)
	},
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop the Ferry service and remove it",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return serviceDo(cmd, service.Uninstall)
	},
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether the Ferry service is installed and running",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return serviceDo(cmd, service.Status)
	},
}

// serviceRunCmd is what the service manager starts. It is the daemon with
// kardianos in charge of its lifetime: answering the Windows service manager,
// or SIGTERM on Unix.
var serviceRunCmd = &cobra.Command{
	Use:    "run",
	Short:  "Run the daemon under the service manager",
	Args:   cobra.NoArgs,
	Hidden: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		env, err := serviceEnv(cmd)
		if err != nil {
			return err
		}
		errOut := cmd.ErrOrStderr()
		prog := &service.Program{
			Run: func(ctx context.Context) error {
				lifetime := func() (context.Context, context.CancelFunc) { return context.WithCancel(ctx) }
				return runDaemon(lifetime, env.Home, errOut, false)
			},
			// The daemon quit on its own (already running, no config, a
			// fatal error). Exit with its code; the service manager decides
			// whether to start it again.
			Exit: func(err error) {
				if err != nil && !errors.Is(err, errSilentExit1) && !errors.Is(err, errExit2) {
					fmt.Fprintln(errOut, "ferry:", err)
				}
				os.Exit(exitCode(err))
			},
		}
		cfg := service.BuildConfig(env.GOOS, service.Params{Executable: env.Executable, Home: env.Home})
		m, err := service.Open(&cfg, prog)
		if err != nil {
			return serviceExit(env, err)
		}
		return m.Run()
	},
}

// serviceDo runs one of the service commands against the real system.
func serviceDo(cmd *cobra.Command, run func(*service.Env) error) error {
	env, err := serviceEnv(cmd)
	if err != nil {
		return err
	}
	return serviceExit(env, run(env))
}

// serviceExit turns the exit codes of internal/service into this command's,
// and reports an unsupported system the way the other commands do.
func serviceExit(env *service.Env, err error) error {
	if errors.Is(err, service.ErrUnsupported) {
		fmt.Fprintln(env.Err, env.Bundle.T(defaultLang(env.Home), "cli.service_unsupported", map[string]any{"Reason": err.Error()}))
		return errExit2
	}
	var ee *service.ExitError
	if errors.As(err, &ee) {
		if ee.Code == 2 {
			return errExit2
		}
		return errSilentExit1
	}
	return err
}

func serviceEnv(cmd *cobra.Command) (*service.Env, error) {
	home, err := filepath.Abs(homeDir())
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return nil, err
	}
	return &service.Env{
		GOOS:       runtime.GOOS,
		Home:       home,
		Executable: exe,
		CurrentUser: func() (string, error) {
			u, err := user.Current()
			if err != nil {
				return "", err
			}
			return u.Username, nil
		},
		Open:     func(cfg *kservice.Config) (service.Manager, error) { return service.Open(cfg, nil) },
		Elevated: service.Elevated,
		ReadPassword: func() (string, error) {
			pw, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(cmd.ErrOrStderr())
			return string(pw), err
		},
		Bundle: defaultBundle(),
		Out:    cmd.OutOrStdout(),
		Err:    cmd.ErrOrStderr(),
	}, nil
}

func init() {
	serviceCmd.AddCommand(serviceInstallCmd, serviceUninstallCmd, serviceStatusCmd, serviceRunCmd)
	rootCmd.AddCommand(serviceCmd)
}
