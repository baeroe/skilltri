package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	for _, content := range []string{"first", "second"} {
		if err := WriteFileAtomic(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(p)
		if err != nil || string(b) != content {
			t.Fatalf("content = %q, %v", b, err)
		}
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left: %v", entries)
	}
}

func TestWriteFileAtomicCleansUp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil { // renaming a file onto a directory fails
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(target, []byte("x"), 0o600); err == nil {
		t.Fatal("want an error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left after error: %v", entries)
	}
}
