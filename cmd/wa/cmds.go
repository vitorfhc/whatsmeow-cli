package main

import (
	"fmt"
	"os"
	"time"

	"github.com/busfactor/whatsmeow-cli/internal/api"
	"github.com/busfactor/whatsmeow-cli/internal/client"
	"github.com/busfactor/whatsmeow-cli/internal/ipc"
	"github.com/spf13/cobra"
)

// emit prints a response and records the exit code.
func emit(resp ipc.Response) {
	code, err := client.PrintResponse(os.Stdout, os.Stderr, resp, flagPretty)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	exitCode = code
}

// emitErr prints an error response and records the exit code.
func emitErr(code, msg string) {
	emit(ipc.Err(code, msg))
}

// runRemote sends a command to the daemon and prints the response. If the
// daemon is unreachable it emits daemon_not_running.
func runRemote(cmd string, args any) {
	p, err := paths()
	if err != nil {
		emitErr(api.ErrGeneric, err.Error())
		return
	}
	req, err := ipc.NewRequest(cmd, args)
	if err != nil {
		emitErr(api.ErrGeneric, err.Error())
		return
	}
	resp, err := client.Call(p.Sock(), req)
	if err != nil {
		emitErr(api.ErrDaemonNotRunning, "daemon not running; run: wa start")
		return
	}
	emit(resp)
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon and connection status",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			p, err := paths()
			if err != nil {
				emitErr(api.ErrGeneric, err.Error())
				return nil
			}
			req, err := ipc.NewRequest("status", nil)
			if err != nil {
				emitErr(api.ErrGeneric, err.Error())
				return nil
			}
			resp, err := client.Call(p.Sock(), req)
			if err != nil {
				// Daemon not running is a valid status answer (exit 0).
				stopped, oerr := ipc.OK(api.StatusResult{Daemon: "stopped"})
				if oerr != nil {
					emitErr(api.ErrGeneric, oerr.Error())
					return nil
				}
				emit(stopped)
				return nil
			}
			emit(resp)
			return nil
		},
	}
}

func newLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login <phone>",
		Short: "Link an account via pairing code",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			runRemote("login", api.LoginArgs{Phone: args[0]})
			return nil
		},
	}
}

func newLogoutCmd() *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Unlink the account",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			runRemote("logout", api.LogoutArgs{Purge: purge})
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also delete stored messages")
	return cmd
}

func newSendCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "send <recipient> <text>",
		Short: "Send a text message",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			runRemote("send", api.SendArgs{Recipient: args[0], Text: args[1]})
			return nil
		},
	}
}

func newMessagesCmd() *cobra.Command {
	var (
		chat     string
		unread   bool
		all      bool
		since    string
		limit    int
		markRead bool
	)
	cmd := &cobra.Command{
		Use:   "messages",
		Short: "List received messages",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			a := api.MessagesArgs{Chat: chat, Unread: unread, All: all, Limit: limit, MarkRead: markRead}
			if since != "" {
				ts, err := time.Parse(time.RFC3339, since)
				if err != nil {
					emitErr(api.ErrUsage, fmt.Sprintf("invalid --since (want RFC3339): %v", err))
					return nil
				}
				a.SinceUnix = ts.Unix()
			}
			runRemote("messages", a)
			return nil
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "filter to one chat (phone or JID)")
	cmd.Flags().BoolVar(&unread, "unread", false, "only unseen messages")
	cmd.Flags().BoolVar(&all, "all", false, "include already-seen messages")
	cmd.Flags().StringVar(&since, "since", "", "only messages at/after this RFC3339 time")
	cmd.Flags().IntVar(&limit, "limit", 0, "max results (default 50)")
	cmd.Flags().BoolVar(&markRead, "mark-read", false, "send WhatsApp read receipts for the results")
	return cmd
}

func newChatsCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "chats",
		Short: "List recent chats",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			runRemote("chats", api.ChatsArgs{Limit: limit})
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "max chats (default 20)")
	return cmd
}
