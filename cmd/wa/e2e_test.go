package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "wa-e2e-bin")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(dir, "wa")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		_ = os.RemoveAll(dir)
		panic("build wa binary: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func runWA(t *testing.T, dataDir string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	full := append([]string{"--data-dir", dataDir}, args...)
	cmd := exec.Command(binPath, full...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if !isExitError(err, &ee) {
			t.Fatalf("run %v: %v", args, err)
		}
		return out.String(), errb.String(), ee.ExitCode()
	}
	return out.String(), errb.String(), 0
}

func isExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return m
}

func TestE2ELifecycle(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() { _, _, _ = runWA(t, dataDir, "stop") })

	// start
	out, _, code := runWA(t, dataDir, "start")
	if code != 0 {
		t.Fatalf("start exit=%d out=%q", code, out)
	}
	if s := decode(t, out)["status"]; s != "started" && s != "already_running" {
		t.Fatalf("start status=%v out=%q", s, out)
	}

	// status: running, not logged in
	out, _, code = runWA(t, dataDir, "status")
	if code != 0 {
		t.Fatalf("status exit=%d", code)
	}
	st := decode(t, out)
	if st["daemon"] != "running" {
		t.Errorf("daemon=%v, want running", st["daemon"])
	}
	if st["logged_in"] != false {
		t.Errorf("logged_in=%v, want false", st["logged_in"])
	}

	// send while not logged in -> not_logged_in, exit 4
	_, errOut, code := runWA(t, dataDir, "send", "5511999999999", "hi")
	if code != 4 {
		t.Errorf("send exit=%d, want 4; stderr=%q", code, errOut)
	}
	if !strings.Contains(errOut, "not_logged_in") {
		t.Errorf("send stderr=%q, want not_logged_in", errOut)
	}

	// messages -> empty array, exit 0
	out, _, code = runWA(t, dataDir, "messages")
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Errorf("messages exit=%d out=%q, want [] and 0", code, out)
	}

	// chats -> empty array, exit 0
	out, _, code = runWA(t, dataDir, "chats")
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Errorf("chats exit=%d out=%q, want [] and 0", code, out)
	}

	// stop -> stopped, exit 0
	out, _, code = runWA(t, dataDir, "stop")
	if code != 0 {
		t.Fatalf("stop exit=%d out=%q", code, out)
	}
	if decode(t, out)["status"] != "stopped" {
		t.Errorf("stop status=%v, want stopped", decode(t, out)["status"])
	}

	// status after stop -> stopped, exit 0
	out, _, code = runWA(t, dataDir, "status")
	if code != 0 {
		t.Fatalf("status(after stop) exit=%d", code)
	}
	if decode(t, out)["daemon"] != "stopped" {
		t.Errorf("after stop daemon=%v, want stopped", decode(t, out)["daemon"])
	}
}

func TestE2EUsageErrors(t *testing.T) {
	dataDir := t.TempDir()

	// send with too few args -> usage, exit 2
	if _, _, code := runWA(t, dataDir, "send", "onlyone"); code != 2 {
		t.Errorf("send missing arg exit=%d, want 2", code)
	}
	// unknown command -> usage, exit 2
	if _, _, code := runWA(t, dataDir, "definitely-not-a-command"); code != 2 {
		t.Errorf("unknown command exit=%d, want 2", code)
	}
}

func TestE2ECommandsWithoutDaemon(t *testing.T) {
	dataDir := t.TempDir()

	// messages with no daemon -> daemon_not_running, exit 3
	_, errOut, code := runWA(t, dataDir, "messages")
	if code != 3 {
		t.Errorf("messages(no daemon) exit=%d, want 3; stderr=%q", code, errOut)
	}
	if !strings.Contains(errOut, "daemon_not_running") {
		t.Errorf("stderr=%q, want daemon_not_running", errOut)
	}

	// status with no daemon -> stopped, exit 0
	out, _, code := runWA(t, dataDir, "status")
	if code != 0 {
		t.Errorf("status(no daemon) exit=%d, want 0", code)
	}
	if decode(t, out)["daemon"] != "stopped" {
		t.Errorf("status(no daemon) daemon=%v, want stopped", decode(t, out)["daemon"])
	}
}
