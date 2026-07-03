package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/busfactor/whatsmeow-cli/internal/api"
	"github.com/busfactor/whatsmeow-cli/internal/client"
	"github.com/busfactor/whatsmeow-cli/internal/config"
	"github.com/busfactor/whatsmeow-cli/internal/daemon"
	"github.com/busfactor/whatsmeow-cli/internal/ipc"
	"github.com/busfactor/whatsmeow-cli/internal/store"
	"github.com/busfactor/whatsmeow-cli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	_ "github.com/mattn/go-sqlite3"
)

const socketReadyTimeout = 10 * time.Second

func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the background daemon",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			runStart()
			return nil
		},
	}
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the background daemon",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			runStop()
			return nil
		},
	}
}

func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__daemon__",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			// A daemon runtime failure is a generic error (exit 1), not a usage
			// error (exit 2, which main uses for cobra Execute errors).
			if err := runDaemon(); err != nil {
				_, _ = fmt.Fprintln(os.Stderr, err)
				exitCode = 1
			}
			return nil
		},
	}
}

func runStart() {
	p, err := paths()
	if err != nil {
		emitErr(api.ErrGeneric, err.Error())
		return
	}
	if daemonRunning(p) {
		emit(mustOK(api.StartResult{Status: "already_running", PID: readPID(p), Socket: p.Sock()}))
		return
	}
	if err := p.EnsureDir(); err != nil {
		emitErr(api.ErrGeneric, err.Error())
		return
	}
	exe, err := os.Executable()
	if err != nil {
		emitErr(api.ErrGeneric, fmt.Sprintf("locate executable: %v", err))
		return
	}
	logFile, err := os.OpenFile(p.Log(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		emitErr(api.ErrGeneric, fmt.Sprintf("open log: %v", err))
		return
	}

	cmd := exec.Command(exe, "__daemon__", "--data-dir", p.Dir)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		emitErr(api.ErrGeneric, fmt.Sprintf("start daemon: %v", err))
		return
	}
	_ = logFile.Close()
	pid := cmd.Process.Pid

	// Wait for the socket, but fail fast (and reap) if the child dies during
	// startup instead of blocking the full timeout.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	if err := waitForSocketOrExit(p, exited, socketReadyTimeout); err != nil {
		emitErr(api.ErrGeneric, err.Error())
		return
	}
	emit(mustOK(api.StartResult{Status: "started", PID: pid, Socket: p.Sock()}))
}

// waitForSocketOrExit polls until the daemon's socket answers, the child
// process exits, or the timeout elapses.
func waitForSocketOrExit(p config.Paths, exited <-chan error, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if daemonRunning(p) {
			return nil
		}
		select {
		case err := <-exited:
			if err != nil {
				return fmt.Errorf("daemon exited during startup: %v (see daemon.log)", err)
			}
			return fmt.Errorf("daemon exited during startup (see daemon.log)")
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("daemon did not become ready")
}

func runStop() {
	p, err := paths()
	if err != nil {
		emitErr(api.ErrGeneric, err.Error())
		return
	}
	req, err := ipc.NewRequest("stop", nil)
	if err != nil {
		emitErr(api.ErrGeneric, err.Error())
		return
	}
	resp, err := client.Call(p.Sock(), req)
	if err != nil {
		emit(mustOK(api.StopResult{Status: "not_running"}))
		return
	}
	emit(resp)
}

// runDaemon is the foreground server started by `wa start`.
func runDaemon() error {
	p, err := paths()
	if err != nil {
		return err
	}
	if err := p.EnsureDir(); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	waLogger := waLog.Stdout("whatsmeow", "INFO", true)

	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	d, cleanup, err := buildDaemon(ctx, p, logger, waLogger)
	if err != nil {
		return err
	}
	defer cleanup()

	// Don't take over from a daemon already live on the socket (start races /
	// double-start). Only a stale socket file should be replaced.
	if daemonRunning(p) {
		logger.Info("another daemon is already running; exiting")
		return nil
	}
	if err := os.Remove(p.Sock()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", p.Sock())
	if err != nil {
		return fmt.Errorf("listen on socket: %w", err)
	}
	if err := os.WriteFile(p.PID(), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("write pid file: %w", err)
	}

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	serveErr := d.Serve(context.Background(), ln)
	d.Close()
	// The unix listener already unlinked the socket on Close; removing it here
	// could delete a newer daemon's socket after a fast stop+start. Remove the
	// pid file only if it still names this process.
	if readPID(p) == os.Getpid() {
		_ = os.Remove(p.PID())
	}
	if serveErr != nil {
		return fmt.Errorf("serve: %w", serveErr)
	}
	return nil
}

// buildDaemon opens the shared database, wires whatsmeow, and constructs the
// daemon. The returned cleanup closes the database.
func buildDaemon(ctx context.Context, p config.Paths, logger *slog.Logger, waLogger waLog.Logger) (*daemon.Daemon, func(), error) {
	dsn := "file:" + p.DB() + "?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	// whatsmeow's sqlstore and our message store share this one handle and both
	// write. SQLite allows a single writer; serialize all access through one
	// connection so concurrent writes never fail with "database is locked".
	db.SetMaxOpenConns(1)

	container := sqlstore.NewWithDB(db, "sqlite3", waLogger)
	if err := container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("upgrade whatsmeow schema: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("get device: %w", err)
	}
	cl := wa.NewRealClient(whatsmeow.NewClient(device, waLogger))
	st, err := store.Open(db)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("open message store: %w", err)
	}
	d := daemon.New(cl, st, logger)

	if _, paired := cl.OwnID(); paired {
		if err := cl.Connect(ctx); err != nil {
			logger.Warn("initial connect failed; relying on auto-reconnect", "err", err)
		}
	}
	return d, func() { _ = db.Close() }, nil
}

func daemonRunning(p config.Paths) bool {
	req, err := ipc.NewRequest("status", nil)
	if err != nil {
		return false
	}
	resp, err := client.Call(p.Sock(), req)
	return err == nil && resp.OK
}

func readPID(p config.Paths) int {
	b, err := os.ReadFile(p.PID())
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}

func mustOK(data any) ipc.Response {
	return ipc.MustOK(data)
}
