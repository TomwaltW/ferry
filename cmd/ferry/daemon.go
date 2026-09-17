package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/TomwaltW/ferry/internal/core"
	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/spf13/cobra"
)

const (
	lockName      = "ferry.lock"
	statusName    = "status.json"
	statusEvery   = 5 * time.Second
	logMaxBytes   = 10 << 20
	logKeepCopies = 3
)

// statusFile is what `ferry status` reads. WrittenAt is stamped by the daemon
// so staleness does not depend on what the core reports.
type statusFile struct {
	core.Status
	WrittenAt time.Time `json:"written_at"`
}

var errAlreadyRunning = errors.New("daemon already running")

// pidLock is a single-instance lock holding the owner's PID. A lock naming a
// process that is gone is treated as stale and taken over.
type pidLock struct{ path string }

func acquireLock(path string) (*pidLock, int, error) {
	if data, err := os.ReadFile(path); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 && processAlive(pid) {
			return nil, pid, errAlreadyRunning
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, 0, err
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return nil, 0, err
	}
	return &pidLock{path: path}, 0, nil
}

func (l *pidLock) release() {
	if l != nil {
		_ = os.Remove(l.path)
	}
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// rotatingWriter keeps <path> under max bytes, renaming it to .1, .2 … and
// dropping anything past keep.
type rotatingWriter struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

func newRotatingWriter(path string, max int64, keep int) (*rotatingWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	w := &rotatingWriter{path: path, max: max, keep: keep}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, info.Size()
	return nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(p)) > w.max {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", w.path, w.keep))
	for i := w.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		return err
	}
	return w.open()
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

var daemonForeground bool

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the Ferry daemon",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		home := homeDir()
		bundle := defaultBundle()

		lock, holder, err := acquireLock(filepath.Join(home, lockName))
		if errors.Is(err, errAlreadyRunning) {
			cmd.PrintErrln(bundle.T(defaultLang(home), "cli.daemon_running", map[string]any{"PID": holder}))
			return errSilentExit1
		}
		if err != nil {
			return err
		}
		defer lock.release()

		logFile, err := newRotatingWriter(filepath.Join(home, "logs", "ferry.log"), logMaxBytes, logKeepCopies)
		if err != nil {
			return err
		}
		defer logFile.Close()
		var sink io.Writer = logFile
		if daemonForeground {
			sink = io.MultiWriter(logFile, cmd.ErrOrStderr())
		}
		log := slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelInfo}))

		a, err := assemble(home, log)
		if err != nil {
			if errors.Is(err, fsp.ErrNotFound) {
				cmd.PrintErrln(bundle.T(defaultLang(home), "cli.need_init", nil))
				return errSilentExit1
			}
			// A database that will not open is fatal and exits with code 2.
			if strings.Contains(err.Error(), "open database") {
				cmd.PrintErrln("ferry:", err)
				return errExit2
			}
			return err
		}
		defer a.Close()

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		// core.Run owns the outbox loop; starting a second one here would
		// give the same outbox two competing senders.
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			writeStatusLoop(ctx, a, filepath.Join(home, statusName))
		}()

		log.Info("ferry daemon started", "home", home, "pid", os.Getpid())
		err = a.Daemon.Run(ctx)
		stop()
		wg.Wait()
		_ = os.Remove(filepath.Join(home, statusName))
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		log.Info("ferry daemon stopped")
		return nil
	},
}

func writeStatusLoop(ctx context.Context, a *assembly, path string) {
	t := time.NewTicker(statusEvery)
	defer t.Stop()
	for {
		writeStatus(a, path)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func writeStatus(a *assembly, path string) {
	data, err := json.MarshalIndent(statusFile{Status: a.Daemon.Status(), WrittenAt: time.Now()}, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func init() {
	daemonCmd.Flags().BoolVar(&daemonForeground, "foreground", true, "also write logs to stderr")
	rootCmd.AddCommand(daemonCmd)
}
