package shim

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/daemon"
)

// tmpDir is a short dir under /tmp (macOS limits socket paths to 104 bytes).
func tmpDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// startDaemonLater returns a start func that runs an in-process daemon after delay, stopped at test end.
// skilltri's own dirs go below dir, so the daemon never touches the real ones.
func startDaemonLater(t *testing.T, dir string, delay time.Duration, calls *atomic.Int32) func() error {
	for _, env := range []string{"SKILLTRI_CONFIG_DIR", "SKILLTRI_STATE_DIR", "SKILLTRI_DATA_DIR", "SKILLTRI_CACHE_DIR"} {
		t.Setenv(env, filepath.Join(dir, env))
	}
	return func() error {
		calls.Add(1)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			time.Sleep(delay)
			d := daemon.New(daemon.Options{
				ConfigPath: filepath.Join(dir, "config.json"),
				SocketPath: filepath.Join(dir, "d.sock"),
				LockPath:   filepath.Join(dir, "d.lock"),
				Version:    "test",
			})
			if err := d.Run(ctx); err != nil {
				t.Errorf("daemon Run: %v", err)
			}
		}()
		t.Cleanup(func() { cancel(); <-done })
		return nil
	}
}

func statusOver(t *testing.T, conn net.Conn) daemon.StatusReply {
	t.Helper()
	defer conn.Close()
	b, _ := json.Marshal(daemon.Hello{V: 1, Kind: "control", Cmd: "status"})
	if _, err := conn.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	var resp struct {
		Error  string
		Result daemon.StatusReply
	}
	if err := json.Unmarshal(line, &resp); err != nil || resp.Error != "" {
		t.Fatalf("status reply %q: %v", line, err)
	}
	return resp.Result
}

func TestDialStartsDaemon(t *testing.T) {
	dir := tmpDir(t)
	var calls atomic.Int32
	conn, err := Dial(filepath.Join(dir, "d.sock"), startDaemonLater(t, dir, 300*time.Millisecond, &calls))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("start called %d times, want 1", n)
	}
	if st := statusOver(t, conn); st.PID != os.Getpid() {
		t.Errorf("status PID = %d, want %d", st.PID, os.Getpid())
	}
}

func TestDialStaleSocket(t *testing.T) {
	dir := tmpDir(t)
	sock := filepath.Join(dir, "d.sock")
	// A socket file with no listener, as a crashed daemon leaves it.
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("stale socket missing: %v", err)
	}
	var calls atomic.Int32
	conn, err := Dial(sock, startDaemonLater(t, dir, 0, &calls))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("start called %d times, want 1", n)
	}
	statusOver(t, conn)
}

func TestDialRunningDaemonDoesNotStart(t *testing.T) {
	dir := tmpDir(t)
	var calls atomic.Int32
	conn, err := Dial(filepath.Join(dir, "d.sock"), startDaemonLater(t, dir, 0, &calls))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	conn, err = Dial(filepath.Join(dir, "d.sock"), func() error { t.Error("start called with a running daemon"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestDialStartError(t *testing.T) {
	dir := tmpDir(t)
	boom := errors.New("boom")
	_, err := Dial(filepath.Join(dir, "d.sock"), func() error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("Dial err = %v, want %v", err, boom)
	}
}

// echoServer accepts one connection, reads the hello line and echoes everything after it until
// EOF, then closes. The hello is sent on hello.
func echoServer(t *testing.T, sock string) <-chan daemon.Hello {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	hello := make(chan daemon.Hello, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		line, err := br.ReadBytes('\n')
		if err != nil {
			return
		}
		var h daemon.Hello
		_ = json.Unmarshal(line, &h)
		hello <- h
		_, _ = io.Copy(conn, br) // returns on the shim's CloseWrite
	}()
	return hello
}

func TestRunRelaysAndClosesWrite(t *testing.T) {
	sock := filepath.Join(tmpDir(t), "e.sock")
	hello := echoServer(t, sock)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader("{\"jsonrpc\":\"2.0\"}\nsecond line\n")
	var out bytes.Buffer
	h := daemon.Hello{V: 1, Kind: "session", Dir: "/demo/project", DirExplicit: true}
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), in, &out, conn, h) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after stdin EOF")
	}
	if got := <-hello; got.V != 1 || got.Kind != "session" || got.Dir != "/demo/project" || !got.DirExplicit {
		t.Errorf("hello = %+v", got)
	}
	if want := "{\"jsonrpc\":\"2.0\"}\nsecond line\n"; out.String() != want {
		t.Errorf("out = %q, want %q", out.String(), want)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	sock := filepath.Join(tmpDir(t), "e.sock")
	echoServer(t, sock)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe() // stdin that never ends
	defer inW.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, inR, io.Discard, conn, daemon.Hello{V: 1, Kind: "session", Dir: "/demo"}) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
