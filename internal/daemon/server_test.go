package daemon

import (
	"bufio"
	"context"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"github.com/busfactor/whatsmeow-cli/internal/api"
	"github.com/busfactor/whatsmeow-cli/internal/ipc"
)

func startServer(t *testing.T, d *Daemon) (string, chan error) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Serve(context.Background(), ln) }()
	return sock, done
}

func roundTrip(t *testing.T, sock, cmd string, args any) ipc.Response {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	req, err := ipc.NewRequest(cmd, args)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := ipc.Write(conn, req); err != nil {
		t.Fatalf("write: %v", err)
	}
	var resp ipc.Response
	if err := ipc.Read(bufio.NewReader(conn), &resp); err != nil {
		t.Fatalf("read: %v", err)
	}
	return resp
}

func TestServeStatusAndStop(t *testing.T) {
	d := newTestDaemon(t, &fakeClient{})
	sock, done := startServer(t, d)

	resp := roundTrip(t, sock, "status", nil)
	if !resp.OK {
		t.Fatalf("status resp: %+v", resp)
	}

	stopResp := roundTrip(t, sock, "stop", nil)
	if !stopResp.OK {
		t.Fatalf("stop resp: %+v", stopResp)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
}

func TestServeConcurrentRequests(t *testing.T) {
	d := newTestDaemon(t, &fakeClient{paired: true})
	sock, done := startServer(t, d)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			resp := roundTrip(t, sock, "send", api.SendArgs{Recipient: "5511999999999", Text: "hi"})
			if !resp.OK {
				t.Errorf("send resp: %+v", resp)
			}
		}()
	}
	wg.Wait()

	roundTrip(t, sock, "stop", nil)
	if err := <-done; err != nil {
		t.Fatalf("Serve error: %v", err)
	}
}
