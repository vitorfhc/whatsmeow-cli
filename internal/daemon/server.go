package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/vitorfhc/whatsmeow-cli/internal/api"
	"github.com/vitorfhc/whatsmeow-cli/internal/ipc"
)

// requestTimeout bounds a single request's handling so a stalled network
// operation cannot leak a goroutine forever. It is shorter than the CLI's
// callTimeout so the daemon returns a proper error response first.
const requestTimeout = 100 * time.Second

// Serve accepts connections and handles one request per connection until the
// listener is closed or a "stop" command is received. Each connection is
// served on its own goroutine; the daemon's shared state is concurrency-safe.
func (d *Daemon) Serve(ctx context.Context, ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go d.serveConn(ctx, conn, ln)
	}
}

func (d *Daemon) serveConn(ctx context.Context, conn net.Conn, ln net.Listener) {
	defer func() { _ = conn.Close() }()

	var req ipc.Request
	if err := ipc.Read(bufio.NewReader(conn), &req); err != nil {
		d.log.Error("read request", "err", err)
		return
	}

	if req.Cmd == "stop" {
		if err := ipc.Write(conn, ok(api.StopResult{Status: "stopped"})); err != nil {
			d.log.Error("write stop response", "err", err)
		}
		d.log.Info("stop requested")
		if err := ln.Close(); err != nil {
			d.log.Error("close listener", "err", err)
		}
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := ipc.Write(conn, d.Handle(reqCtx, req)); err != nil {
		d.log.Error("write response", "err", err)
	}
}

// Close cancels the daemon-lifetime context (stopping any background QR
// session) and disconnects the WhatsApp client. Call after Serve returns.
func (d *Daemon) Close() {
	d.cancel()
	d.client.Disconnect()
}
