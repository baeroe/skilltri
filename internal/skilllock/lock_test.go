package skilllock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func hash(t *testing.T, dir string) string {
	t.Helper()
	h, err := HashDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHashDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo-skill")
	write(t, filepath.Join(dir, "SKILL.md"), "---\nname: demo-skill\n---\n", 0o644)
	write(t, filepath.Join(dir, "scripts", "run.sh"), "echo demo\n", 0o755)
	if err := os.Symlink("scripts/run.sh", filepath.Join(dir, "run")); err != nil {
		t.Fatal(err)
	}

	base := hash(t, dir)
	if base == "" || hash(t, dir) != base {
		t.Fatal("hash not stable")
	}
	// an identical copy elsewhere hashes the same (relative paths only)
	other := filepath.Join(t.TempDir(), "copy")
	write(t, filepath.Join(other, "SKILL.md"), "---\nname: demo-skill\n---\n", 0o644)
	write(t, filepath.Join(other, "scripts", "run.sh"), "echo demo\n", 0o755)
	if err := os.Symlink("scripts/run.sh", filepath.Join(other, "run")); err != nil {
		t.Fatal(err)
	}
	if hash(t, other) != base {
		t.Fatal("identical copy hashes differently")
	}

	changes := []struct {
		name string
		do   func()
	}{
		{"content", func() { write(t, filepath.Join(dir, "SKILL.md"), "---\nname: demo-skill\n---\nmore\n", 0o644) }},
		{"mode", func() { write(t, filepath.Join(dir, "scripts", "run.sh"), "echo demo\n", 0o644) }},
		{"symlink target", func() {
			os.Remove(filepath.Join(dir, "run"))
			if err := os.Symlink("SKILL.md", filepath.Join(dir, "run")); err != nil {
				t.Fatal(err)
			}
		}},
		{"new file", func() { write(t, filepath.Join(dir, "notes.txt"), "", 0o644) }},
		{"rename", func() { os.Rename(filepath.Join(dir, "notes.txt"), filepath.Join(dir, "notes2.txt")) }},
	}
	prev := base
	for _, c := range changes {
		c.do()
		h := hash(t, dir)
		if h == prev {
			t.Fatalf("%s: hash unchanged", c.name)
		}
		prev = h
	}

	if _, err := HashDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing dir: no error")
	}
}

func TestLockRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "skills.lock")
	l, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if l.Version != 1 || l.Sources == nil || len(l.Sources) != 0 {
		t.Fatalf("missing lock: %+v", l)
	}

	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	l.Sources["demo-git"] = Source{Commit: "0123456789abcdef0123456789abcdef01234567", FetchedAt: at,
		Skills: map[string]string{"demo-skill": "abc"}}
	l.Sources["demo-market/plugins/demo-plugin"] = Source{FetchedAt: at, Error: "fetch failed",
		Skills: map[string]string{"demo-plugin:other-skill": "def"}}
	if err := Save(path, l); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || len(got.Sources) != 2 {
		t.Fatalf("loaded %+v", got)
	}
	s := got.Sources["demo-git"]
	if s.Commit != l.Sources["demo-git"].Commit || !s.FetchedAt.Equal(at) || s.Skills["demo-skill"] != "abc" {
		t.Fatalf("demo-git = %+v", s)
	}
	if e := got.Sources["demo-market/plugins/demo-plugin"]; e.Error != "fetch failed" || e.Commit != "" {
		t.Fatalf("plugin source = %+v", e)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("leftover temp files: %v", entries)
	}

	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("broken lock: no error")
	}
}

func TestPath(t *testing.T) {
	t.Setenv("SKILLTRI_STATE_DIR", "/demo/state")
	if got := Path(); got != "/demo/state/skills.lock" {
		t.Fatalf("Path = %s", got)
	}
}
