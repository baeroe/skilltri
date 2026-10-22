package shim

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestDaemonEnv(t *testing.T) {
	environ := []string{
		"HOME=/home/demo", "USER=demo", "LOGNAME=demo", "LANG=en_US.UTF-8", "TMPDIR=/tmp/demo/",
		"SHELL=/bin/demo-sh", "PATH=/work/project/.mise/bin:/usr/bin",
		"CLAUDECODE=1", "DEMO_TOKEN=secret", "LC_CTYPE=C",
		"SKILLTRI_STATE_DIR=/state", "SKILLTRI_SOCKET=/tmp/d.sock", "SKILLTRI_CWD=/work/project",
	}
	var gotShell string
	login := func(shell string) (string, error) { gotShell = shell; return "/login/bin:/usr/bin", nil }
	want := []string{
		"HOME=/home/demo", "LANG=en_US.UTF-8", "LOGNAME=demo", "PATH=/login/bin:/usr/bin",
		"SHELL=/bin/demo-sh", "SKILLTRI_SOCKET=/tmp/d.sock", "SKILLTRI_STATE_DIR=/state",
		"TMPDIR=/tmp/demo/", "USER=demo",
	}
	if got := daemonEnv(environ, login); !slices.Equal(got, want) {
		t.Fatalf("daemonEnv =\n%q\nwant\n%q", got, want)
	}
	if gotShell != "/bin/demo-sh" {
		t.Errorf("login shell = %q", gotShell)
	}

	withLC := append(slices.Clone(environ), "LC_ALL=de_DE.UTF-8")
	if got := daemonEnv(withLC, login); !slices.Contains(got, "LC_ALL=de_DE.UTF-8") {
		t.Errorf("LC_ALL missing: %q", got)
	}
}

func TestDaemonEnvFallsBackToCurrentPath(t *testing.T) {
	for name, login := range map[string]func(string) (string, error){
		"error": func(string) (string, error) { return "", errors.New("boom") },
		"empty": func(string) (string, error) { return "", nil },
	} {
		t.Run(name, func(t *testing.T) {
			got := daemonEnv([]string{"SHELL=/bin/demo-sh", "PATH=/cur/bin"}, login)
			if !slices.Contains(got, "PATH=/cur/bin") {
				t.Fatalf("daemonEnv = %q", got)
			}
		})
	}
	// Without SHELL there is no login shell to ask.
	got := daemonEnv([]string{"PATH=/cur/bin"}, func(string) (string, error) { t.Error("login shell asked"); return "", nil })
	if !slices.Equal(got, []string{"PATH=/cur/bin"}) {
		t.Fatalf("daemonEnv = %q", got)
	}
}

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "demo-shell")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoginShellPath(t *testing.T) {
	// A login shell whose profile prints a banner and sets PATH; $1 is -lc, $2 the command.
	sh := script(t, `[ "$1" = -lc ] || exit 9; echo "welcome"; PATH=/login/bin:/usr/bin; eval "$2"`)
	got, err := loginShellPath(sh)
	if err != nil || got != "/login/bin:/usr/bin" {
		t.Fatalf("loginShellPath = %q, %v", got, err)
	}
}

func TestLoginShellPathTimeout(t *testing.T) {
	old := loginPathTimeout
	loginPathTimeout = 200 * time.Millisecond
	t.Cleanup(func() { loginPathTimeout = old })
	start := time.Now()
	if _, err := loginShellPath(script(t, "exec sleep 10")); err == nil {
		t.Fatal("hanging shell returned no error")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("timeout took %v", d)
	}
}
