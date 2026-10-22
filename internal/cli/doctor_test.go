package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baeroe/skilltri/internal/doctor"
)

// doctorEnv points every skilltri path and HOME at temp dirs, with the shell hook set.
func doctorEnv(t *testing.T, config string) {
	t.Helper()
	offline(t, config)
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("SKILLTRI_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("SKILLTRI_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("SKILLTRI_HOOK", "zsh")
}

func TestDoctorExitCode(t *testing.T) {
	doctorEnv(t, `{"version":1,"servers":{}}`)
	var out bytes.Buffer
	if code := Doctor(&out, false); code != 0 {
		t.Errorf("exit = %d, want 0 (only the daemon warning):\n%s", code, out.String())
	}
	if s := out.String(); !strings.Contains(s, "! ") || !strings.Contains(s, "daemon not running") || !strings.Contains(s, "    fix: ") {
		t.Errorf("output:\n%s", s)
	}

	doctorEnv(t, `{bad`)
	out.Reset()
	if code := Doctor(&out, false); code != 1 {
		t.Errorf("exit = %d, want 1 on a config error", code)
	}
	if s := out.String(); !strings.Contains(s, "✗ ") || strings.Index(s, "✗") > strings.Index(s, "!") {
		t.Errorf("errors not first:\n%s", s)
	}

	out.Reset()
	if code := Doctor(&out, true); code != 1 {
		t.Errorf("json exit = %d, want 1", code)
	}
	var fs []doctor.Finding
	if err := json.Unmarshal(out.Bytes(), &fs); err != nil || len(fs) == 0 || fs[0].Severity != "error" || fs[0].Check != "config" {
		t.Errorf("json = %s (%v)", out.String(), err)
	}
}

func TestDoctorCleanJSON(t *testing.T) {
	doctorEnv(t, "")
	os.Unsetenv("SKILLTRI_HOOK")
	var out bytes.Buffer
	if code := Doctor(&out, true); code != 0 {
		t.Errorf("exit = %d", code)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "[") {
		t.Errorf("json = %s", out.String())
	}
}
