// Package client is the thin CLI side: it dials the daemon socket, sends one
// request, and prints the response as JSON with a mapped exit code.
package client

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/busfactor/whatsmeow-cli/internal/api"
	"github.com/busfactor/whatsmeow-cli/internal/ipc"
)

const (
	dialTimeout = 3 * time.Second
	// callTimeout bounds the whole request so a stuck daemon (e.g. a network
	// operation that never returns) cannot hang the CLI forever. It is longer
	// than the daemon's own per-request timeout so the daemon's error response
	// wins under normal stalls.
	callTimeout = 120 * time.Second
)

// Call dials the daemon's Unix socket, sends req, and returns the response.
// A dial error means the daemon is not running.
func Call(sockPath string, req ipc.Request) (ipc.Response, error) {
	conn, err := net.DialTimeout("unix", sockPath, dialTimeout)
	if err != nil {
		return ipc.Response{}, fmt.Errorf("connect to daemon: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(callTimeout)); err != nil {
		return ipc.Response{}, fmt.Errorf("set deadline: %w", err)
	}
	if err := ipc.Write(conn, req); err != nil {
		return ipc.Response{}, fmt.Errorf("send request: %w", err)
	}
	var resp ipc.Response
	if err := ipc.Read(bufio.NewReader(conn), &resp); err != nil {
		return ipc.Response{}, fmt.Errorf("read response: %w", err)
	}
	return resp, nil
}

type errPayload struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// PrintResponse writes a success payload to stdout or an error payload to
// stderr and returns the process exit code (see spec §10).
func PrintResponse(stdout, stderr io.Writer, resp ipc.Response, pretty bool) (int, error) {
	if resp.OK {
		if err := writeJSON(stdout, resp.Data, pretty); err != nil {
			return 1, err
		}
		return 0, nil
	}
	payload, err := ipc.Marshal(errPayload{Error: resp.Error, Message: resp.Message})
	if err != nil {
		return 1, fmt.Errorf("marshal error payload: %w", err)
	}
	if err := writeJSON(stderr, payload, pretty); err != nil {
		return api.ExitCode(resp.Error), err
	}
	return api.ExitCode(resp.Error), nil
}

// writeJSON writes raw JSON (or "{}" if empty) followed by a newline,
// optionally indented.
func writeJSON(w io.Writer, raw json.RawMessage, pretty bool) error {
	out := []byte(raw)
	if len(out) == 0 {
		out = []byte("{}")
	}
	if pretty {
		var buf bytes.Buffer
		if err := json.Indent(&buf, out, "", "  "); err != nil {
			return fmt.Errorf("indent json: %w", err)
		}
		out = buf.Bytes()
	}
	out = append(out, '\n')
	if _, err := w.Write(out); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
