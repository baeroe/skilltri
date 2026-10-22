package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/daemon"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// demoRepo is a local git repo with the skill "alpha"; it returns the folder and HEAD.
func demoRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR", "GIT_COMMITTER"} {
		t.Setenv(k+"_NAME", "Demo Author")
		t.Setenv(k+"_EMAIL", "demo@example.com")
	}
	dir := filepath.Join(t.TempDir(), "upstream")
	if err := os.MkdirAll(filepath.Join(dir, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("---\nname: alpha\ndescription: Demo skill alpha.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "initial")
	return dir, git(t, dir, "rev-parse", "HEAD")
}

func TestCLIUpdateOffline(t *testing.T) {
	repo, head := demoRepo(t)
	offline(t, `{"version":1,"sources":{"demo":{"type":"git","url":"file://`+repo+`","updateEvery":"0"},`+
		`"gone":{"type":"git","url":"file://`+filepath.Join(repo, "missing")+`"}}}`)
	base := t.TempDir()
	for _, env := range []string{"SKILLTRI_STATE_DIR", "SKILLTRI_DATA_DIR", "SKILLTRI_CACHE_DIR"} {
		t.Setenv(env, filepath.Join(base, env))
	}

	var out bytes.Buffer
	if code := Update(&out, []string{"demo"}, false); code != 0 {
		t.Fatalf("exit = %d, output %q", code, out.String())
	}
	if s := out.String(); !strings.Contains(s, "demo") || !strings.Contains(s, head[:7]) || strings.Contains(s, head[:8]) || strings.Contains(s, "gone") {
		t.Errorf("output = %q", s)
	}
	c, err := catalog.Load(catalog.Path())
	if err != nil {
		t.Fatalf("catalog not saved: %v", err)
	}
	if len(c.Skills) != 1 || c.Skills[0].ID != "alpha" {
		t.Errorf("skills = %+v", c.Skills)
	}

	out.Reset()
	if code := Update(&out, nil, true); code != 1 {
		t.Fatalf("exit = %d with a failing source, want 1; output %q", code, out.String())
	}
	var r daemon.UpdateReply
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if len(r.Sources) != 2 || r.Sources[0].Name != "demo" || r.Sources[0].Commit != head || r.Sources[1].Name != "gone" || r.Sources[1].Error == "" {
		t.Errorf("reply = %+v", r)
	}
	if !strings.Contains(out.String(), "\n  ") {
		t.Error("JSON is not indented")
	}
}
