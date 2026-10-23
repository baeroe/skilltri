package actions

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/rules"
)

// shortSocket returns a socket path short enough for macOS (t.TempDir paths can exceed 104 bytes).
func shortSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sta")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

// fakeSocket accepts control connections on sock; answer returns the reply line for a hello, or ""
// to never answer (a hung daemon).
func fakeSocket(t *testing.T, sock string, answer func(daemon.Hello) string) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadBytes('\n')
				if err != nil {
					return
				}
				var h daemon.Hello
				_ = json.Unmarshal(line, &h)
				if r := answer(h); r != "" {
					c.Write([]byte(r + "\n"))
					return
				}
				<-done
			}()
		}
	}()
}

func TestActionsDoNotHangOnAHungDaemon(t *testing.T) {
	e := testEnv(t)
	e.Socket = shortSocket(t)
	fakeSocket(t, e.Socket, func(daemon.Hello) string { return "" })
	start := time.Now()
	if err := ServerAdd(e, "docs", config.Server{Type: "stdio", Command: "docs-mcp"}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("ServerAdd with a hung daemon took %v (default control timeout must be 2 s)", d)
	}

	e.ControlTimeout = 200 * time.Millisecond
	start = time.Now()
	if _, err := RuleAdd(e, parse(t, "server/docs/tool/search"), rules.Rule{Path: "/x", Mode: rules.Tree}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("RuleAdd with a hung daemon took %v with a 200 ms control timeout", d)
	}
}

func TestUnknownToolAsksForEnvDir(t *testing.T) {
	e := testEnv(t)
	e.Socket = shortSocket(t)
	e.Dir = filepath.Join(t.TempDir(), "proj")
	asked := make(chan string, 4)
	fakeSocket(t, e.Socket, func(h daemon.Hello) string {
		if h.Cmd != "which" {
			return `{"result":{}}`
		}
		asked <- h.Args[0]
		return `{"result":{"Servers":[{"Name":"docs","State":"ready","Tools":[{"Name":"search"}]}]}}`
	})
	if err := ServerAdd(e, "docs", config.Server{Type: "stdio", Command: "docs-mcp"}); err != nil {
		t.Fatal(err)
	}
	w, err := RuleAdd(e, parse(t, "server/docs/tool/serch"), rules.Rule{Path: "/x", Mode: rules.Tree})
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 1 {
		t.Fatalf("warnings = %q", w)
	}
	if got := <-asked; got != rules.Normalize(e.Dir) {
		t.Fatalf("which asked for %q, want %q", got, rules.Normalize(e.Dir))
	}
}
