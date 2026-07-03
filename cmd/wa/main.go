// Command wa is a WhatsApp CLI backed by a local daemon that holds the
// whatsmeow connection. See docs/superpowers/specs for the design.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/vitorfhc/whatsmeow-cli/internal/config"
)

// exitCode is set by commands and used as the process exit code.
var exitCode int

var (
	flagDataDir string
	flagPretty  bool
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		// cobra already reported usage/argument errors.
		os.Exit(2)
	}
	os.Exit(exitCode)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "wa",
		Short:         "WhatsApp CLI (daemon-backed)",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.PersistentFlags().StringVar(&flagDataDir, "data-dir", "", "data directory (default $WA_CLI_HOME or ~/.wa-cli)")
	root.PersistentFlags().BoolVar(&flagPretty, "pretty", false, "pretty-print JSON output")

	root.AddCommand(
		newStartCmd(),
		newStopCmd(),
		newStatusCmd(),
		newLoginCmd(),
		newLoginQRCmd(),
		newLogoutCmd(),
		newSendCmd(),
		newMessagesCmd(),
		newChatsCmd(),
		newDaemonCmd(),
	)
	return root
}

// paths resolves the data directory from flags/env.
func paths() (config.Paths, error) {
	p, err := config.Resolve(flagDataDir)
	if err != nil {
		return config.Paths{}, fmt.Errorf("resolve data dir: %w", err)
	}
	return p, nil
}
