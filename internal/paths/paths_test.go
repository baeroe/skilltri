package paths

import (
	"path/filepath"
	"testing"
)

func TestDirsEnvOverride(t *testing.T) {
	cases := []struct {
		env string
		fn  func() string
	}{
		{"SKILLTRI_CONFIG_DIR", ConfigDir},
		{"SKILLTRI_STATE_DIR", StateDir},
		{"SKILLTRI_DATA_DIR", DataDir},
		{"SKILLTRI_CACHE_DIR", CacheDir},
	}
	for _, c := range cases {
		t.Run(c.env, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", "/ignored")
			t.Setenv("XDG_STATE_HOME", "/ignored")
			t.Setenv("XDG_DATA_HOME", "/ignored")
			t.Setenv("XDG_CACHE_HOME", "/ignored")
			t.Setenv(c.env, "/a")
			if got := c.fn(); got != "/a" {
				t.Fatalf("got %q, want /a", got)
			}
		})
	}
}

func clearEnv(t *testing.T) {
	for _, k := range []string{"SKILLTRI_CONFIG_DIR", "SKILLTRI_STATE_DIR", "SKILLTRI_DATA_DIR", "SKILLTRI_CACHE_DIR", "SKILLTRI_SOCKET"} {
		t.Setenv(k, "")
	}
}

func TestDirsXDG(t *testing.T) {
	clearEnv(t)
	t.Setenv("XDG_CONFIG_HOME", "/x")
	t.Setenv("XDG_STATE_HOME", "/x")
	t.Setenv("XDG_DATA_HOME", "/x")
	t.Setenv("XDG_CACHE_HOME", "/x")
	for name, got := range map[string]string{"config": ConfigDir(), "state": StateDir(), "data": DataDir(), "cache": CacheDir()} {
		if got != "/x/skilltri" {
			t.Errorf("%s: got %q, want /x/skilltri", name, got)
		}
	}
}

func TestDirsDefault(t *testing.T) {
	clearEnv(t)
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", "/home/demo")
	want := map[string]string{
		"config": "/home/demo/.config/skilltri",
		"state":  "/home/demo/.local/state/skilltri",
		"data":   "/home/demo/.local/share/skilltri",
		"cache":  "/home/demo/.cache/skilltri",
	}
	got := map[string]string{"config": ConfigDir(), "state": StateDir(), "data": DataDir(), "cache": CacheDir()}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %q, want %q", k, got[k], w)
		}
	}
}

func TestSocketOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv("SKILLTRI_STATE_DIR", "/st")
	if got := SocketPath(); got != "/st/daemon.sock" {
		t.Fatalf("default socket %q", got)
	}
	t.Setenv("SKILLTRI_SOCKET", "/s.sock")
	if got := SocketPath(); got != "/s.sock" {
		t.Fatalf("override socket %q", got)
	}
}

func TestDerivedFiles(t *testing.T) {
	clearEnv(t)
	t.Setenv("SKILLTRI_CONFIG_DIR", "/c")
	t.Setenv("SKILLTRI_STATE_DIR", "/s")
	if got, want := ConfigFile(), filepath.Join("/c", "config.json"); got != want {
		t.Errorf("ConfigFile %q", got)
	}
	if got := DaemonLock(); got != "/s/daemon.lock" {
		t.Errorf("DaemonLock %q", got)
	}
	if got := LogFile(); got != "/s/daemon.log" {
		t.Errorf("LogFile %q", got)
	}
}

func TestAuditLog(t *testing.T) {
	clearEnv(t)
	t.Setenv("SKILLTRI_STATE_DIR", "/s")
	if got := AuditLog(); got != filepath.Join("/s", "audit.jsonl") {
		t.Fatalf("AuditLog() = %q", got)
	}
}
