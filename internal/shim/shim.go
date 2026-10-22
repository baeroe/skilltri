// Package shim is `skilltri mcp`, the stdio process an agent harness starts. It connects to the
// daemon's Unix socket, starting a detached daemon when none answers, sends the session hello and
// then only relays bytes: all MCP logic lives in the daemon, so the shim stays tiny and stateless.
package shim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/paths"
)

// Dial retry timing after start: the daemon gets dialTimeout to accept connections.
const (
	retryInterval = 100 * time.Millisecond
	dialTimeout   = 5 * time.Second
)

// Dial connects to the daemon at socket. If no daemon listens (no socket file, or a stale one
// left by a crashed daemon), it calls start once and retries every 100 ms for up to 5 s. When
// several shims race, every one starts a daemon; the losers of the daemon lock exit and all
// shims reach the winner.
func Dial(socket string, start func() error) (net.Conn, error) {
	conn, err := net.Dial("unix", socket)
	if err == nil {
		return conn, nil
	}
	if !errors.Is(err, syscall.ENOENT) && !errors.Is(err, syscall.ECONNREFUSED) {
		return nil, err
	}
	if err := start(); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}
	deadline := time.Now().Add(dialTimeout)
	for {
		time.Sleep(retryInterval)
		conn, err := net.Dial("unix", socket)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("daemon did not accept on %s within %s: %w", socket, dialTimeout, err)
		}
	}
}

// StartDetached starts `exe daemon` in its own session, so it outlives the shim and the
// harness. stdin is /dev/null and stdout and stderr append to the daemon log. It runs in the
// home directory, which downstream servers without a cwd inherit, not in the harness's project,
// and with daemonEnv instead of the harness's environment.
func StartDetached(exe string) error {
	if err := os.MkdirAll(paths.StateDir(), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(paths.LogFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer null.Close()

	cmd := exec.Command(exe, "daemon")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, logf, logf
	cmd.Env = daemonEnv(os.Environ(), loginShellPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	} else {
		cmd.Dir = "/"
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap the child when it exits (a daemon that loses the lock race exits at once); the
	// goroutine ends with the shim otherwise.
	go func() { _ = cmd.Wait() }()
	return nil
}

// Run writes h as the hello line on conn and relays in → conn and conn → out. When in hits EOF,
// it half-closes conn so the daemon ends the session; Run returns when the daemon's side ends or
// ctx is cancelled, and closes conn. A clean end returns nil.
func Run(ctx context.Context, in io.Reader, out io.Writer, conn net.Conn, h daemon.Hello) error {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}
	// This goroutine may stay blocked on in after Run returns; the process exits right after.
	go func() {
		if _, err := io.Copy(conn, in); err != nil {
			conn.Close()
			return
		}
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			conn.Close()
		}
	}()
	if _, err := io.Copy(out, conn); err != nil && ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}
