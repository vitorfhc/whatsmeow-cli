// Package config resolves the data directory and the file paths the daemon
// and CLI share (SQLite DB, Unix socket, pid file, log file).
package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnvHome is the environment variable that overrides the default data dir.
const EnvHome = "WA_CLI_HOME"

// Paths holds the resolved data directory and derives the files within it.
type Paths struct {
	Dir string
}

// Resolve determines the data directory using the precedence:
// flagDir (if non-empty) > $WA_CLI_HOME > $HOME/.wa-cli.
func Resolve(flagDir string) (Paths, error) {
	if flagDir != "" {
		return Paths{Dir: flagDir}, nil
	}
	if env := os.Getenv(EnvHome); env != "" {
		return Paths{Dir: env}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}
	return Paths{Dir: filepath.Join(home, ".wa-cli")}, nil
}

// DB returns the SQLite database path.
func (p Paths) DB() string { return filepath.Join(p.Dir, "store.db") }

// Sock returns the Unix domain socket path.
func (p Paths) Sock() string { return filepath.Join(p.Dir, "daemon.sock") }

// PID returns the daemon pid file path.
func (p Paths) PID() string { return filepath.Join(p.Dir, "daemon.pid") }

// Log returns the daemon log file path.
func (p Paths) Log() string { return filepath.Join(p.Dir, "daemon.log") }

// EnsureDir creates the data directory (0700) if it does not exist.
func (p Paths) EnsureDir() error {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return fmt.Errorf("create data dir %q: %w", p.Dir, err)
	}
	return nil
}
