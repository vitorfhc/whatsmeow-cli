package client

import (
	"bufio"
	"bytes"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vitorfhc/whatsmeow-cli/internal/ipc"
)

func TestPrintResponseOK(t *testing.T) {
	resp, err := ipc.OK(map[string]string{"id": "3EB0"})
	if err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	code, err := PrintResponse(&out, &errBuf, resp, false)
	if err != nil {
		t.Fatalf("PrintResponse: %v", err)
	}
	if code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), `"id":"3EB0"`) {
		t.Errorf("stdout = %q", out.String())
	}
	if errBuf.Len() != 0 {
		t.Errorf("stderr should be empty, got %q", errBuf.String())
	}
}

func TestPrintResponseEmptyData(t *testing.T) {
	resp := ipc.Response{OK: true}
	var out, errBuf bytes.Buffer
	code, err := PrintResponse(&out, &errBuf, resp, false)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
	if strings.TrimSpace(out.String()) != "{}" {
		t.Errorf("stdout = %q, want {}", out.String())
	}
}

func TestPrintResponseError(t *testing.T) {
	resp := ipc.Err("send_failed", "boom")
	var out, errBuf bytes.Buffer
	code, err := PrintResponse(&out, &errBuf, resp, false)
	if err != nil {
		t.Fatal(err)
	}
	if code != 7 {
		t.Errorf("code = %d, want 7", code)
	}
	if out.Len() != 0 {
		t.Errorf("stdout should be empty on error, got %q", out.String())
	}
	if !strings.Contains(errBuf.String(), `"error":"send_failed"`) ||
		!strings.Contains(errBuf.String(), `"message":"boom"`) {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestCallRoundTrip(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req ipc.Request
		if err := ipc.Read(bufio.NewReader(conn), &req); err != nil {
			return
		}
		resp, _ := ipc.OK(map[string]string{"echo": req.Cmd})
		_ = ipc.Write(conn, resp)
	}()

	req, _ := ipc.NewRequest("status", nil)
	resp, err := Call(sock, req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !resp.OK {
		t.Fatalf("resp not ok: %+v", resp)
	}
	var got map[string]string
	if err := resp.Bind(&got); err != nil {
		t.Fatal(err)
	}
	if got["echo"] != "status" {
		t.Errorf("echo = %q, want status", got["echo"])
	}
}

func TestCallDaemonDown(t *testing.T) {
	req, _ := ipc.NewRequest("status", nil)
	_, err := Call(filepath.Join(t.TempDir(), "nonexistent.sock"), req)
	if err == nil {
		t.Error("expected error dialing missing socket, got nil")
	}
}
