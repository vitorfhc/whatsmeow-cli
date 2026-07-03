package config

import (
	"path/filepath"
	"testing"
)

func TestResolveDefault(t *testing.T) {
	t.Setenv("WA_CLI_HOME", "")
	t.Setenv("HOME", "/home/tester")

	p, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := "/home/tester/.wa-cli"
	if p.Dir != want {
		t.Errorf("Dir = %q, want %q", p.Dir, want)
	}
}

func TestResolveEnvOverridesDefault(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("WA_CLI_HOME", "/custom/env/dir")

	p, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.Dir != "/custom/env/dir" {
		t.Errorf("Dir = %q, want /custom/env/dir", p.Dir)
	}
}

func TestResolveFlagOverridesEnv(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("WA_CLI_HOME", "/custom/env/dir")

	p, err := Resolve("/explicit/flag/dir")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.Dir != "/explicit/flag/dir" {
		t.Errorf("Dir = %q, want /explicit/flag/dir", p.Dir)
	}
}

func TestPathsDerivedFiles(t *testing.T) {
	p := Paths{Dir: "/data"}
	cases := map[string]string{
		p.DB():   filepath.Join("/data", "store.db"),
		p.Sock(): filepath.Join("/data", "daemon.sock"),
		p.PID():  filepath.Join("/data", "daemon.pid"),
		p.Log():  filepath.Join("/data", "daemon.log"),
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("derived path = %q, want %q", got, want)
		}
	}
}

func TestEnsureDirCreates(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "wa-cli")
	p := Paths{Dir: dir}
	if err := p.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	if err := p.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir (idempotent): %v", err)
	}
}
