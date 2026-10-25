package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendReadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state", "audit.jsonl")
	if got, err := Read(p, 10); err != nil || len(got) != 0 {
		t.Fatalf("Read of a missing log = %v, %v", got, err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for i := range 3 {
		e := Entry{Time: now.Add(time.Duration(i) * time.Second), Tool: "skilltri_add_server", Target: fmt.Sprintf("demo-%d", i),
			Summary: "added", Client: "demo-client", Dir: "/work/demo", OK: i != 1}
		if i == 1 {
			e.Error = "boom"
		}
		if err := Append(p, e); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	for _, key := range []string{`"time"`, `"tool"`, `"target"`, `"summary"`, `"client"`, `"dir"`, `"ok"`, `"error"`} {
		if !strings.Contains(lines[0], key) {
			t.Errorf("line %s misses %s", lines[0], key)
		}
	}
	got, err := Read(p, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Target != "demo-1" || got[1].Target != "demo-2" || got[0].OK || got[0].Error != "boom" || !got[1].OK {
		t.Fatalf("Read(2) = %+v", got)
	}
	if !got[1].Time.Equal(now.Add(2 * time.Second)) {
		t.Fatalf("time = %v", got[1].Time)
	}
	all, _ := Read(p, 0)
	if len(all) != 3 {
		t.Fatalf("Read(0) = %d entries, want all 3", len(all))
	}
}

func TestReadSkipsBadLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(p, []byte("not json\n{\"tool\":\"skilltri_x\",\"ok\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(p, 5)
	if err != nil || len(got) != 1 || got[0].Tool != "skilltri_x" {
		t.Fatalf("Read = %+v, %v", got, err)
	}
}

func TestConcurrentAppends(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	const n = 100
	long := strings.Repeat("x", 8<<10) // longer than PIPE_BUF: atomicity comes from the lock
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Append(p, Entry{Tool: "skilltri_t", Target: fmt.Sprint(i), Summary: long, OK: true}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := Read(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("got %d entries, want %d", len(got), n)
	}
	seen := map[string]bool{}
	for _, e := range got {
		if e.Summary != long {
			t.Fatal("interleaved line")
		}
		seen[e.Target] = true
	}
	if len(seen) != n {
		t.Fatalf("%d distinct targets", len(seen))
	}
}
