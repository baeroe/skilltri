package cli

import (
	"fmt"
	"io"
	"net"
	"time"

	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/paths"
)

// stopTimeout bounds the wait for the daemon's socket to stop answering after shutdown.
const stopTimeout = 10 * time.Second

// Stop asks the daemon to shut down and waits until it no longer answers. It returns 0 when the
// daemon stopped or was not running.
func Stop(w io.Writer) int {
	sock := paths.SocketPath()
	err := daemon.Control(sock, daemon.Hello{Cmd: "shutdown"}, nil)
	switch {
	case notRunning(err):
		fmt.Fprintln(w, "daemon not running")
		return 0
	case err != nil:
		fmt.Fprintf(errOut, "skilltri: daemon stop: %v\n", err)
		return 1
	}
	deadline := time.Now().Add(stopTimeout)
	for {
		conn, err := net.Dial("unix", sock)
		if notRunning(err) {
			fmt.Fprintln(w, "daemon stopped")
			return 0
		}
		if conn != nil {
			conn.Close()
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(errOut, "skilltri: daemon stop: still answering on %s after %s\n", sock, stopTimeout)
			return 1
		}
		time.Sleep(50 * time.Millisecond)
	}
}
