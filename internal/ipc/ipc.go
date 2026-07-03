// Package ipc defines the newline-delimited JSON protocol used between the
// CLI and the daemon over the Unix domain socket. Each connection carries one
// request line and one response line.
package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Marshal is like json.Marshal but does not HTML-escape <, >, and &, keeping
// message text readable and compact for the AI agent consuming the output.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Request is a single command sent from the CLI to the daemon.
type Request struct {
	Cmd  string          `json:"cmd"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Response is the daemon's reply. On success OK is true and Data holds the
// result; on failure OK is false and Error/Message describe the problem.
type Response struct {
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
	Message string          `json:"message,omitempty"`
}

// NewRequest builds a request, marshaling args (which may be nil) into Args.
func NewRequest(cmd string, args any) (Request, error) {
	req := Request{Cmd: cmd}
	if args != nil {
		raw, err := Marshal(args)
		if err != nil {
			return Request{}, fmt.Errorf("marshal args: %w", err)
		}
		req.Args = raw
	}
	return req, nil
}

// Bind unmarshals the request args into v. It is a no-op when there are no args.
func (r Request) Bind(v any) error {
	if len(r.Args) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Args, v); err != nil {
		return fmt.Errorf("unmarshal args: %w", err)
	}
	return nil
}

// OK builds a success response, marshaling data (which may be nil) into Data.
func OK(data any) (Response, error) {
	resp := Response{OK: true}
	if data != nil {
		raw, err := Marshal(data)
		if err != nil {
			return Response{}, fmt.Errorf("marshal data: %w", err)
		}
		resp.Data = raw
	}
	return resp, nil
}

// Err builds a failure response with a machine-readable code and a concise
// human message.
func Err(code, msg string) Response {
	return Response{OK: false, Error: code, Message: msg}
}

// MustOK is OK without a returned error: on the (near-impossible) marshal
// failure it yields a generic error response instead. Use where a caller
// cannot act on a marshal error.
func MustOK(data any) Response {
	resp, err := OK(data)
	if err != nil {
		return Err("generic", "marshal result: "+err.Error())
	}
	return resp
}

// Bind unmarshals the response data into v. It is a no-op when there is no data.
func (resp Response) Bind(v any) error {
	if len(resp.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Data, v); err != nil {
		return fmt.Errorf("unmarshal data: %w", err)
	}
	return nil
}

// Write marshals v and writes it as one newline-terminated line.
func Write(w io.Writer, v any) error {
	raw, err := Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	raw = append(raw, '\n')
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// Read reads one newline-terminated line and unmarshals it into v. A message
// without a trailing newline followed by EOF is still accepted; a genuine
// transport error is surfaced rather than masked as a JSON error.
func Read(br *bufio.Reader, v any) error {
	line, readErr := br.ReadBytes('\n')
	if len(line) == 0 {
		return fmt.Errorf("read: %w", readErr)
	}
	if err := json.Unmarshal(line, v); err != nil {
		if readErr != nil {
			return fmt.Errorf("read: %w", readErr)
		}
		return fmt.Errorf("unmarshal: %w", err)
	}
	return nil
}
