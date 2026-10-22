package downstream

import (
	"strings"
	"sync"
)

// stderrLines is how many stderr lines of a downstream process are kept for Status.
const stderrLines = 50

// ring is an io.Writer that keeps the last stderrLines complete lines written to it.
type ring struct {
	mu      sync.Mutex
	lines   []string
	partial string
}

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.partial + string(p)
	parts := strings.Split(s, "\n")
	r.partial = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		r.lines = append(r.lines, strings.TrimRight(l, "\r"))
	}
	if len(r.lines) > stderrLines {
		r.lines = append([]string(nil), r.lines[len(r.lines)-stderrLines:]...)
	}
	return len(p), nil
}

// Lines returns a copy of the kept lines, including an unterminated last line.
func (r *ring) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.lines...)
	if r.partial != "" {
		out = append(out, r.partial)
	}
	if len(out) > stderrLines {
		out = out[len(out)-stderrLines:]
	}
	return out
}
