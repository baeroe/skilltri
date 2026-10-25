package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/audit"
	"github.com/baeroe/skilltri/internal/paths"
)

func TestAudit(t *testing.T) {
	t.Setenv("SKILLTRI_STATE_DIR", t.TempDir())
	var out bytes.Buffer
	if code := Audit(&out, 20, false); code != 0 || strings.TrimSpace(out.String()) != "no agent changes recorded" {
		t.Fatalf("empty: %d %q", code, out.String())
	}
	out.Reset()
	if code := Audit(&out, 20, true); code != 0 || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("empty json: %d %q", code, out.String())
	}
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for i := range 25 {
		e := audit.Entry{Time: at.Add(time.Duration(i) * time.Minute), Tool: "skilltri_set_enabled", Target: fmt.Sprintf("server/demo-%d", i),
			Client: "demo-client", Dir: "/work/demo", OK: true}
		if i == 23 {
			e.Client = "demo\x1b]0;title\x07\nclient"
		}
		if i == 24 {
			e.Tool, e.OK, e.Error = "skilltri_remove_source", false, `no such source "demo-24"`
		}
		if err := audit.Append(paths.AuditLog(), e); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	if code := Audit(&out, 20, false); code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 20 {
		t.Fatalf("%d lines:\n%s", len(lines), out.String())
	}
	first := strings.Fields(lines[0])
	if want := at.Add(5 * time.Minute).Local().Format(time.DateTime); first[0]+" "+first[1] != want ||
		first[2] != "demo-client" || first[3] != "skilltri_set_enabled" || first[4] != "server/demo-5" || first[5] != "ok" {
		t.Errorf("first line = %q", lines[0])
	}
	if strings.ContainsAny(out.String(), "\x1b\x07") || !strings.Contains(lines[18], "demo ]0;title  client") {
		t.Errorf("control characters not removed: %q", lines[18])
	}
	if last := lines[19]; !strings.Contains(last, "skilltri_remove_source") || !strings.Contains(last, `error: no such source "demo-24"`) {
		t.Errorf("last line = %q", last)
	}
	out.Reset()
	if code := Audit(&out, 2, true); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var es []audit.Entry
	if err := json.Unmarshal(out.Bytes(), &es); err != nil || len(es) != 2 || es[1].Target != "server/demo-24" || es[1].OK {
		t.Fatalf("json = %s (%v)", out.String(), err)
	}
}
