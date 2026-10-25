// Package audit is the append-only log of the changes agents make through skilltri's management
// tools: one JSON object per line in StateDir/audit.jsonl. It never records env or header values.
package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one write tool call.
type Entry struct {
	Time    time.Time `json:"time"`
	Tool    string    `json:"tool"`
	Target  string    `json:"target"`
	Summary string    `json:"summary"`
	Client  string    `json:"client"`
	Dir     string    `json:"dir"`
	OK      bool      `json:"ok"`
	Error   string    `json:"error"`
}

// mu serializes appends within the process; the flock covers other processes.
var mu sync.Mutex

// Append writes e as one line to path (created 0600, its folder 0700). A zero Time is set to now.
func Append(path string, e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	unlock, err := lock(f)
	if err != nil {
		f.Close()
		return err
	}
	_, werr := f.Write(b)
	unlock()
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// Read returns the last n entries of path, oldest first (n <= 0: all). A missing file has none;
// lines that do not parse are skipped.
func Read(path string, n int) ([]Entry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		var e Entry
		if len(line) > 0 && json.Unmarshal(line, &e) == nil {
			out = append(out, e)
			if n > 0 && len(out) > 2*n { // keep memory bounded on long logs
				out = append(out[:0], out[len(out)-n:]...)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}
