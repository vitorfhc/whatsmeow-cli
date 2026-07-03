package ipc

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	type sendArgs struct {
		Recipient string `json:"recipient"`
		Text      string `json:"text"`
	}
	req, err := NewRequest("send", sendArgs{Recipient: "111", Text: "hi"})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	var buf bytes.Buffer
	if err := Write(&buf, req); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		t.Error("framed message must end with newline")
	}

	var got Request
	if err := Read(bufio.NewReader(&buf), &got); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Cmd != "send" {
		t.Errorf("Cmd = %q, want send", got.Cmd)
	}
	var args sendArgs
	if err := got.Bind(&args); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if args.Recipient != "111" || args.Text != "hi" {
		t.Errorf("args = %+v, want {111 hi}", args)
	}
}

func TestResponseOK(t *testing.T) {
	type result struct {
		ID string `json:"id"`
	}
	resp, err := OK(result{ID: "abc"})
	if err != nil {
		t.Fatalf("OK: %v", err)
	}
	if !resp.OK {
		t.Error("resp.OK = false, want true")
	}

	var buf bytes.Buffer
	if err := Write(&buf, resp); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var got Response
	if err := Read(bufio.NewReader(&buf), &got); err != nil {
		t.Fatalf("Read: %v", err)
	}
	var r result
	if err := got.Bind(&r); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if r.ID != "abc" {
		t.Errorf("id = %q, want abc", r.ID)
	}
}

func TestResponseErr(t *testing.T) {
	resp := Err("send_failed", "boom")
	if resp.OK {
		t.Error("error response must have OK=false")
	}
	if resp.Error != "send_failed" || resp.Message != "boom" {
		t.Errorf("resp = %+v, want error=send_failed message=boom", resp)
	}
}

func TestWriteDoesNotHTMLEscape(t *testing.T) {
	resp := Err("send_failed", "run: wa login <phone> & retry")
	var buf bytes.Buffer
	if err := Write(&buf, resp); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "\\u003c") || strings.Contains(out, "\\u0026") {
		t.Errorf("output is HTML-escaped: %q", out)
	}
	if !strings.Contains(out, "<phone> & retry") {
		t.Errorf("literal chars missing: %q", out)
	}
}

func TestReadEmptyReturnsError(t *testing.T) {
	var got Request
	if err := Read(bufio.NewReader(&bytes.Buffer{}), &got); err == nil {
		t.Error("Read on empty stream expected error, got nil")
	}
}

func TestRequestBindNoArgs(t *testing.T) {
	req, err := NewRequest("status", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	// Binding when there are no args should be a no-op, not an error.
	var v struct{}
	if err := req.Bind(&v); err != nil {
		t.Errorf("Bind with no args: %v", err)
	}
}
