// Package skilllock reads and writes the skill lock file: per source the checked-out commit,
// the last fetch and its error, and a content hash per skill.
package skilllock

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/baeroe/skilltri/internal/fsutil"
	"github.com/baeroe/skilltri/internal/paths"
)

// Lock is the content of skills.lock, keyed by source key.
type Lock struct {
	Version int               `json:"version"`
	Sources map[string]Source `json:"sources"`
}

// Source is the lock entry of one source; Skills maps skill id → HashDir of its folder.
type Source struct {
	Commit    string            `json:"commit,omitempty"`
	FetchedAt time.Time         `json:"fetchedAt"`
	Error     string            `json:"error,omitempty"`
	Skills    map[string]string `json:"skills"`
}

// Path returns the lock file location: paths.StateDir()/skills.lock.
func Path() string { return filepath.Join(paths.StateDir(), "skills.lock") }

// Load reads the lock at path. A missing file yields an empty version-1 lock.
func Load(path string) (*Lock, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Lock{Version: 1, Sources: map[string]Source{}}, nil
	}
	if err != nil {
		return nil, err
	}
	l := &Lock{}
	if err := json.Unmarshal(b, l); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if l.Version == 0 {
		l.Version = 1
	}
	if l.Sources == nil {
		l.Sources = map[string]Source{}
	}
	return l, nil
}

// Save writes l to path atomically with mode 0600, creating the folder if needed.
func Save(path string, l *Lock) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(b, '\n'), 0o600)
}

// HashDir returns a hex sha256 over every entry below dir in sorted order: its relative path,
// its file mode and its content (regular files) or target string (symlinks, never followed).
func HashDir(dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%o\x00", filepath.ToSlash(rel), uint32(info.Mode()))
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%d\x00%s", len(target), target)
		case info.Mode().IsRegular():
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			fmt.Fprintf(h, "%d\x00", info.Size())
			if _, err := io.Copy(h, f); err != nil {
				return err
			}
		}
		h.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
