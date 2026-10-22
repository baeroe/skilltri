package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// helloVersion is the only hello protocol version.
const helloVersion = 1

// maxHello bounds the hello line; it is also the buffer size of the reader handed to the session.
const maxHello = 64 << 10

// Hello is the first line (JSON + "\n") a client writes on the socket.
type Hello struct {
	V           int      `json:"v"`                     // 1
	Kind        string   `json:"kind"`                  // "session" | "control"
	Dir         string   `json:"dir,omitempty"`         // session folder
	DirExplicit bool     `json:"dirExplicit,omitempty"` // from --cwd/SKILLTRI_CWD: roots never override it
	Cmd         string   `json:"cmd,omitempty"`         // control: "status" | "which"
	Args        []string `json:"args,omitempty"`
}

// readHello reads and checks the hello line from r. The bytes after the newline stay in r.
func readHello(r *bufio.Reader) (Hello, error) {
	line, err := r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return Hello{}, errors.New("hello line too long")
	}
	if err != nil {
		return Hello{}, fmt.Errorf("read hello: %w", err)
	}
	var h Hello
	if err := json.Unmarshal(line, &h); err != nil {
		return Hello{}, fmt.Errorf("parse hello: %w", err)
	}
	if h.V != helloVersion {
		return h, fmt.Errorf("unsupported hello version %d (want %d)", h.V, helloVersion)
	}
	if h.Kind != "session" && h.Kind != "control" {
		return h, fmt.Errorf("unknown hello kind %q", h.Kind)
	}
	return h, nil
}

// bufferedConn reads through the bufio.Reader that consumed the hello, so no buffered bytes are
// lost, and closes the underlying connection.
type bufferedConn struct {
	*bufio.Reader
	io.Closer
}
