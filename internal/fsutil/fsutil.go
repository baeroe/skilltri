// Package fsutil holds small file system helpers shared by skilltri's stores.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic replaces path with data: it writes a temp file next to path with mode perm,
// syncs it to disk and renames it into place, so readers see either the old or the new content.
// On error the temp file is removed and path is left as it was.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil { // the rename must never publish a file whose data is not on disk
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
